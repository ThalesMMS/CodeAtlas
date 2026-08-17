package store

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
)

// recoverAndLoad selects and loads the authoritative snapshot at Open time. It
// removes stale temps, prefers a valid official file, and only falls back to a
// validated backup (created by a non-atomic-replace platform) when the official
// is missing or corrupt. It never selects a file by mtime alone, and never
// accepts a partially decoded snapshot.
func (s *Store) recoverAndLoad() error {
	s.persister.cleanStaleTemps()

	data, err := s.persister.fs.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !s.tryRestoreBackup() {
			return os.ErrNotExist // a missing snapshot is a valid empty initial state
		}
		data, err = s.persister.fs.ReadFile(s.path)
		if err != nil {
			return err
		}
	}

	snapshot, decodeErr := decodeSnapshot(data)
	if decodeErr != nil {
		if !s.tryRestoreBackup() {
			return decodeErr // corrupt official and no valid backup -> rebuild required
		}
		restored, readErr := s.persister.fs.ReadFile(s.path)
		if readErr != nil {
			return readErr
		}
		snapshot, decodeErr = decodeSnapshot(restored)
		if decodeErr != nil {
			return decodeErr
		}
	}

	return s.applySnapshot(snapshot)
}

// tryRestoreBackup promotes a valid backup to the official path. It returns false
// when no valid backup exists (the normal case on POSIX, where atomic rename
// leaves none). An invalid backup is never accepted.
func (s *Store) tryRestoreBackup() bool {
	backup := s.persister.backupPath()
	data, err := s.persister.fs.ReadFile(backup)
	if err != nil {
		return false
	}
	if _, decodeErr := decodeSnapshot(data); decodeErr != nil {
		return false
	}
	return s.persister.fs.Rename(backup, s.path) == nil
}

// applySnapshot loads a decoded snapshot into the active state, deriving or
// validating the edge-id counter, and rebuilds the derived indexes. A snapshot
// lacking the counter is repaired; a new-format snapshot with inconsistent edge
// ids is rejected.
func (s *Store) applySnapshot(snapshot diskSnapshot) error {
	st := s.current
	// A pre-v1 (line-based id) snapshot cannot be converted heuristically: back it
	// up and start empty so the initial index rebuilds the whole workspace under
	// the new identity algorithm and publishes atomically. A rebuild failure is
	// surfaced later by the indexer/readiness, leaving the backup in place.
	if snapshot.SnapshotSchema < snapshotSchema {
		if err := s.backupLegacySnapshot(snapshot.SnapshotSchema); err != nil {
			return err
		}
		slog.Warn("legacy index snapshot backed up; rebuilding from scratch under the new identity model",
			"storedSchema", snapshot.SnapshotSchema, "currentSchema", snapshotSchema, "snapshot", s.path)
		s.current = newState()
		return nil
	}
	for _, file := range snapshot.Files {
		st.files[file.Path] = file
	}
	for _, identity := range snapshot.Identities {
		st.identities[identity.ID] = identity
	}
	for _, occurrence := range snapshot.Occurrences {
		st.occurrences[occurrence.ID] = occurrence
		st.occByFile[occurrence.Path] = append(st.occByFile[occurrence.Path], occurrence.ID)
		if occurrence.SymbolID != "" {
			st.occBySymbol[occurrence.SymbolID] = append(st.occBySymbol[occurrence.SymbolID], occurrence.ID)
		}
	}
	st.edges = snapshot.Edges
	for _, page := range snapshot.Wiki {
		st.wiki[page.Slug] = page
	}
	for id, vector := range snapshot.Embeddings {
		st.embeddings[id] = vector
	}
	st.embeddingMetadata = snapshot.EmbeddingMetadata
	st.version = snapshot.StoreVersion
	st.indexedAt = snapshot.IndexedAt

	if snapshot.NextEdgeID == 0 {
		st.nextEdgeID, _ = migrateEdgeIDs(st.edges)
	} else {
		if err := validateEdgeIDs(st.edges, snapshot.NextEdgeID); err != nil {
			return fmt.Errorf("invalid edge ids in snapshot: %w", err)
		}
		st.nextEdgeID = snapshot.NextEdgeID
	}
	st.finalize()
	if err := s.validateLoaded(st); err != nil {
		return err
	}
	return validateSnapshotIntegrity(st, snapshot)
}

// backupLegacySnapshot moves a pre-v1 snapshot aside under a controlled name so
// it is preserved (never deleted) while the workspace is reindexed.
func (s *Store) backupLegacySnapshot(schema int) error {
	backup := fmt.Sprintf("%s.legacy-schema%d.bak", s.path, schema)
	if err := s.persister.fs.Rename(s.path, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// validateSnapshotIntegrity recomputes the content-addressed SnapshotID and
// rejects a snapshot whose stored id does not match its content (corruption). A
// legacy snapshot without an id, or one written under an older schema, is an
// accepted migration: the id is recomputed and persisted on the next commit.
func validateSnapshotIntegrity(st *state, snapshot diskSnapshot) error {
	if snapshot.SnapshotID == "" {
		return nil // legacy snapshot: id is assigned on the next persist
	}
	if snapshot.SnapshotSchema != snapshotSchema {
		slog.Warn("snapshot schema changed; recomputing SnapshotID", "storedSchema", snapshot.SnapshotSchema, "currentSchema", snapshotSchema)
		return nil // explicitly versioned migration
	}
	if computed := computeSnapshotID(st); computed != snapshot.SnapshotID {
		return apperror.StoreCorrupted(fmt.Errorf("snapshot id mismatch: stored %s, computed %s", snapshot.SnapshotID, computed))
	}
	return nil
}

// validateLoaded audits a freshly finalized snapshot. Authoritative corruption
// (non-repairable error) fails Open so the process never reaches READY over a
// silently-wrong index. Repairable derived drift is rebuilt from the
// authoritative entities and revalidated explicitly; warnings are logged.
func (s *Store) validateLoaded(st *state) error {
	violations := ValidateState(st)
	if HasBlockingViolations(violations) {
		return fmt.Errorf("invalid index snapshot: %s", ViolationSummary(violations))
	}
	if hasRepairableViolations(violations) {
		st.finalize() // rebuild derived indexes from authoritative sources
		violations = ValidateState(st)
		if HasBlockingViolations(violations) {
			return fmt.Errorf("invalid index snapshot after derived repair: %s", ViolationSummary(violations))
		}
		slog.Warn("rebuilt derived store indexes during load", "snapshot", s.path)
	}
	if warnings := countWarnings(violations); warnings > 0 {
		slog.Warn("index snapshot loaded with validation warnings", "count", warnings, "summary", ViolationSummary(violations), "snapshot", s.path)
	}
	return nil
}

func hasRepairableViolations(violations []Violation) bool {
	for _, v := range violations {
		if v.Repairable {
			return true
		}
	}
	return false
}

func countWarnings(violations []Violation) int {
	warnings := 0
	for _, v := range violations {
		if v.Severity == SeverityWarning && !v.Repairable {
			warnings++
		}
	}
	return warnings
}
