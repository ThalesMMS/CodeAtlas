package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/textutil"
)

// ArtifactStatus is the mutable freshness of an artifact head.
type ArtifactStatus string

const (
	StatusNotGenerated ArtifactStatus = "not_generated"
	StatusCurrent      ArtifactStatus = "current"
	StatusStale        ArtifactStatus = "stale"
	StatusFailed       ArtifactStatus = "failed"
)

// ArtifactID identifies one immutable artifact version.
type ArtifactID string

const (
	maxArtifactPayloadBytes = 4 << 20 // 4 MiB
	maxErrorMessageBytes    = 1000
)

// ArtifactDependency is one verifiable input the artifact was built from.
type ArtifactDependency struct {
	Kind         string // symbol | occurrence | file | relation | snapshot | config
	SymbolID     string
	OccurrenceID string
	Path         string
	RelationKey  string
	ContentHash  string
}

// ArtifactCandidate is a fully-prepared, validated artifact ready to publish. The
// store does not call an LLM, build prompts or fix output.
type ArtifactCandidate struct {
	Type, Key                                                          string
	InputSnapshotID, ContextPackHash                                   string
	PolicyVersion, PromptVersion, OutputSchemaVersion, RendererVersion string
	Provider, Model, Title                                             string
	Payload, RenderedMarkdown, Metadata                                string
	DurationMillis                                                     int64
	Dependencies                                                       []ArtifactDependency
}

// Artifact is an immutable published version (a copy; never a live row).
type Artifact struct {
	ID                                                                 ArtifactID
	Type, Key                                                          string
	Revision                                                           int
	InputSnapshotID, ContextPackHash                                   string
	PolicyVersion, PromptVersion, OutputSchemaVersion, RendererVersion string
	Provider, Model, Title                                             string
	Payload, RenderedMarkdown, PayloadHash, Metadata                   string
	CreatedAt                                                          time.Time
	Dependencies                                                       []ArtifactDependency
}

// ArtifactHead is the mutable per-(type,key) state.
type ArtifactHead struct {
	Type, Key                       string
	CurrentID                       ArtifactID
	LatestRevision                  int
	Status                          ArtifactStatus
	StaleReason                     string
	LastErrorCode, LastErrorMessage string
	UpdatedAt                       time.Time
}

// ArtifactFilter narrows ListHeads.
type ArtifactFilter struct {
	Type   string
	Status ArtifactStatus
}

// GenerationAttempt describes a failed generation for RecordFailure.
type GenerationAttempt struct {
	Type, Key, InputSnapshotID string
}

// PublishedChangeSet carries the structural keys a commit changed, for granular
// invalidation.
type PublishedChangeSet struct {
	ChangedPaths     []string
	ChangedSymbolIDs []string
	RemovedSymbolIDs []string
	ChangedRelations []string
}

// InvalidationResult lists the heads marked stale.
type InvalidationResult struct {
	StaleHeads []ArtifactHead
}

// ArtifactStore persists DeepWiki/Codemaps artifacts as immutable versions with a
// mutable head per logical key. All SQL stays inside this package.
type ArtifactStore struct {
	db *DB
}

// Artifacts returns the artifact store over the same database.
func (s *Store) Artifacts() *ArtifactStore { return &ArtifactStore{db: s.db} }

// Publish writes a new immutable artifact version and advances the head, atomically.
// Strict v1: if the current structural snapshot moved since the candidate's
// InputSnapshotID, it returns ARTIFACT_INPUT_SNAPSHOT_STALE and publishes nothing.
func (a *ArtifactStore) Publish(ctx context.Context, candidate ArtifactCandidate) (Artifact, error) {
	if candidate.Type == "" || candidate.Key == "" {
		return Artifact{}, apperror.InvalidArgument("artifact type and key are required", nil)
	}
	if candidate.Payload == "" {
		return Artifact{}, apperror.InvalidArgument("artifact payload is required", nil)
	}
	if len(candidate.Payload) > maxArtifactPayloadBytes || len(candidate.RenderedMarkdown) > maxArtifactPayloadBytes {
		return Artifact{}, apperror.RequestTooLarge(maxArtifactPayloadBytes)
	}

	tx, err := a.db.Writer().BeginTx(ctx, nil)
	if err != nil {
		return Artifact{}, apperror.PersistenceFailed(err)
	}
	defer tx.Rollback() //nolint:errcheck

	var currentSnapshot string
	if err := tx.QueryRowContext(ctx, "SELECT snapshot_id FROM repository_state WHERE id = 1").Scan(&currentSnapshot); err != nil {
		return Artifact{}, apperror.StoreCorrupted(err)
	}
	if currentSnapshot != candidate.InputSnapshotID {
		return Artifact{}, apperror.ArtifactInputSnapshotStale()
	}
	if err := revalidateDependencies(ctx, tx, candidate.Dependencies); err != nil {
		return Artifact{}, err
	}

	latest, err := headRevision(ctx, tx, candidate.Type, candidate.Key)
	if err != nil {
		return Artifact{}, err
	}
	nextRevision := latest + 1
	now := time.Now().UTC()
	payloadSum := sha256.Sum256([]byte(candidate.Payload))
	payloadHash := hex.EncodeToString(payloadSum[:])
	artifactID := ArtifactID(deriveArtifactID(candidate.Type, candidate.Key, nextRevision, payloadHash))

	if err := txExec(ctx, tx, `INSERT INTO artifacts
		(artifact_id, artifact_type, logical_key, artifact_revision, input_snapshot_id, context_pack_hash,
		 policy_version, prompt_version, output_schema_version, renderer_version, provider, model, title,
		 payload, rendered_markdown, payload_hash, duration_ms, metadata, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(artifactID), candidate.Type, candidate.Key, nextRevision, candidate.InputSnapshotID, candidate.ContextPackHash,
		candidate.PolicyVersion, candidate.PromptVersion, candidate.OutputSchemaVersion, candidate.RendererVersion,
		candidate.Provider, candidate.Model, candidate.Title, candidate.Payload, candidate.RenderedMarkdown, payloadHash,
		candidate.DurationMillis, nonEmpty(candidate.Metadata, "{}"), now.Format(time.RFC3339Nano)); err != nil {
		return Artifact{}, apperror.PersistenceFailed(err)
	}
	if err := insertDependencies(ctx, tx, artifactID, candidate.Dependencies); err != nil {
		return Artifact{}, err
	}
	if err := txExec(ctx, tx, `INSERT INTO artifact_heads
		(artifact_type, logical_key, current_artifact_id, latest_revision, status, stale_reason, last_error_code, last_error_message, updated_at)
		VALUES(?,?,?,?, 'current', '', '', '', ?)
		ON CONFLICT(artifact_type, logical_key) DO UPDATE SET current_artifact_id=excluded.current_artifact_id,
			latest_revision=excluded.latest_revision, status='current', stale_reason='', last_error_code='', last_error_message='', updated_at=excluded.updated_at`,
		candidate.Type, candidate.Key, string(artifactID), nextRevision, now.Format(time.RFC3339Nano)); err != nil {
		return Artifact{}, apperror.PersistenceFailed(err)
	}
	if err := tx.Commit(); err != nil {
		return Artifact{}, apperror.PersistenceFailed(err)
	}
	return a.Get(ctx, artifactID)
}

// RecordFailure records a generation/validation failure in a SEPARATE transaction
// without inserting a partial artifact. It preserves any current version: with no
// prior artifact the head becomes failed; with a prior current/stale version the
// current pointer is preserved and only the last error is recorded.
func (a *ArtifactStore) RecordFailure(ctx context.Context, attempt GenerationAttempt, appErr *apperror.AppError) error {
	if attempt.Type == "" || attempt.Key == "" {
		return apperror.InvalidArgument("attempt type and key are required", nil)
	}
	code, message := "INTERNAL_ERROR", "generation failed"
	if appErr != nil {
		code = string(appErr.Code)
		message = bound(appErr.Error(), maxErrorMessageBytes)
	}
	tx, err := a.db.Writer().BeginTx(ctx, nil)
	if err != nil {
		return apperror.PersistenceFailed(err)
	}
	defer tx.Rollback() //nolint:errcheck

	head, found, err := readHead(ctx, tx, attempt.Type, attempt.Key)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if !found || head.CurrentID == "" {
		// No valid version: the head is failed.
		if err := txExec(ctx, tx, `INSERT INTO artifact_heads
			(artifact_type, logical_key, current_artifact_id, latest_revision, status, stale_reason, last_error_code, last_error_message, updated_at)
			VALUES(?,?,NULL,0,'failed','',?,?,?)
			ON CONFLICT(artifact_type, logical_key) DO UPDATE SET status='failed', last_error_code=excluded.last_error_code, last_error_message=excluded.last_error_message, updated_at=excluded.updated_at`,
			attempt.Type, attempt.Key, code, message, now); err != nil {
			return apperror.PersistenceFailed(err)
		}
	} else {
		// A valid version exists: preserve current/stale, record the refresh failure.
		if err := txExec(ctx, tx, `UPDATE artifact_heads
			SET last_error_code=?, last_error_message=?, updated_at=?
			WHERE artifact_type=? AND logical_key=?`,
			code, message, now, attempt.Type, attempt.Key); err != nil {
			return apperror.PersistenceFailed(err)
		}
	}
	return tx.Commit()
}

// Invalidate marks heads stale when their current artifact depends on a changed
// structural key (or has a global snapshot dependency). It preserves the current
// artifact and never touches independent heads or the structural revision.
func (a *ArtifactStore) Invalidate(ctx context.Context, change PublishedChangeSet) (InvalidationResult, error) {
	tx, err := a.db.Writer().BeginTx(ctx, nil)
	if err != nil {
		return InvalidationResult{}, apperror.PersistenceFailed(err)
	}
	defer tx.Rollback() //nolint:errcheck

	heads, err := currentHeads(ctx, tx)
	if err != nil {
		return InvalidationResult{}, err
	}
	changedSymbols := toSet(change.ChangedSymbolIDs, change.RemovedSymbolIDs)
	changedPaths := toSet(change.ChangedPaths)
	changedRelations := toSet(change.ChangedRelations)

	result := InvalidationResult{}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, head := range heads {
		reason, stale := dependencyHit(ctx, tx, head.CurrentID, changedSymbols, changedPaths, changedRelations)
		if !stale {
			continue
		}
		if err := txExec(ctx, tx, "UPDATE artifact_heads SET status='stale', stale_reason=?, updated_at=? WHERE artifact_type=? AND logical_key=?",
			reason, now, head.Type, head.Key); err != nil {
			return InvalidationResult{}, apperror.PersistenceFailed(err)
		}
		head.Status = StatusStale
		head.StaleReason = reason
		result.StaleHeads = append(result.StaleHeads, head)
	}
	if err := tx.Commit(); err != nil {
		return InvalidationResult{}, apperror.PersistenceFailed(err)
	}
	return result, nil
}

// GetHead returns the mutable head for a logical key.
func (a *ArtifactStore) GetHead(ctx context.Context, artifactType, key string) (ArtifactHead, error) {
	head, found, err := readHead(ctx, a.db.Reader(), artifactType, key)
	if err != nil {
		return ArtifactHead{}, err
	}
	if !found {
		return ArtifactHead{Type: artifactType, Key: key, Status: StatusNotGenerated}, nil
	}
	return head, nil
}

// Get returns an immutable artifact version with its dependencies.
func (a *ArtifactStore) Get(ctx context.Context, id ArtifactID) (Artifact, error) {
	row := a.db.Reader().QueryRowContext(ctx, `SELECT artifact_id, artifact_type, logical_key, artifact_revision,
		input_snapshot_id, context_pack_hash, policy_version, prompt_version, output_schema_version, renderer_version,
		provider, model, title, payload, rendered_markdown, payload_hash, metadata, created_at
		FROM artifacts WHERE artifact_id = ?`, string(id))
	var artifact Artifact
	var createdRaw string
	err := row.Scan(&artifact.ID, &artifact.Type, &artifact.Key, &artifact.Revision,
		&artifact.InputSnapshotID, &artifact.ContextPackHash, &artifact.PolicyVersion, &artifact.PromptVersion,
		&artifact.OutputSchemaVersion, &artifact.RendererVersion, &artifact.Provider, &artifact.Model, &artifact.Title,
		&artifact.Payload, &artifact.RenderedMarkdown, &artifact.PayloadHash, &artifact.Metadata, &createdRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, apperror.ArtifactNotFound()
	}
	if err != nil {
		return Artifact{}, apperror.StoreCorrupted(err)
	}
	artifact.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
	artifact.Dependencies, err = readDependencies(ctx, a.db.Reader(), id)
	if err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

// ListHeads returns heads matching the filter, ordered by type then key.
func (a *ArtifactStore) ListHeads(ctx context.Context, filter ArtifactFilter) ([]ArtifactHead, error) {
	query := "SELECT artifact_type, logical_key, COALESCE(current_artifact_id,''), latest_revision, status, stale_reason, last_error_code, last_error_message, updated_at FROM artifact_heads"
	var conditions []string
	var args []any
	if filter.Type != "" {
		conditions = append(conditions, "artifact_type = ?")
		args = append(args, filter.Type)
	}
	if filter.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(filter.Status))
	}
	for i, condition := range conditions {
		if i == 0 {
			query += " WHERE "
		} else {
			query += " AND "
		}
		query += condition
	}
	query += " ORDER BY artifact_type, logical_key"
	rows, err := a.db.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, apperror.StoreCorrupted(err)
	}
	defer rows.Close()
	var heads []ArtifactHead
	for rows.Next() {
		head, err := scanHead(rows)
		if err != nil {
			return nil, apperror.StoreCorrupted(err)
		}
		heads = append(heads, head)
	}
	return heads, rows.Err()
}

// --- helpers ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanHead(scanner rowScanner) (ArtifactHead, error) {
	var head ArtifactHead
	var currentID, status, updatedRaw string
	if err := scanner.Scan(&head.Type, &head.Key, &currentID, &head.LatestRevision, &status,
		&head.StaleReason, &head.LastErrorCode, &head.LastErrorMessage, &updatedRaw); err != nil {
		return ArtifactHead{}, err
	}
	head.CurrentID = ArtifactID(currentID)
	head.Status = ArtifactStatus(status)
	head.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
	return head, nil
}

func readHead(ctx context.Context, q rowQueryer, artifactType, key string) (ArtifactHead, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT artifact_type, logical_key, COALESCE(current_artifact_id,''), latest_revision, status, stale_reason, last_error_code, last_error_message, updated_at
		FROM artifact_heads WHERE artifact_type = ? AND logical_key = ?`, artifactType, key)
	if err != nil {
		return ArtifactHead{}, false, apperror.StoreCorrupted(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return ArtifactHead{}, false, rows.Err()
	}
	head, err := scanHead(rows)
	if err != nil {
		return ArtifactHead{}, false, apperror.StoreCorrupted(err)
	}
	return head, true, nil
}

func headRevision(ctx context.Context, tx *sql.Tx, artifactType, key string) (int, error) {
	var latest int
	err := tx.QueryRowContext(ctx, "SELECT latest_revision FROM artifact_heads WHERE artifact_type = ? AND logical_key = ?", artifactType, key).Scan(&latest)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, apperror.StoreCorrupted(err)
	}
	return latest, nil
}

func currentHeads(ctx context.Context, tx *sql.Tx) ([]ArtifactHead, error) {
	rows, err := tx.QueryContext(ctx, `SELECT artifact_type, logical_key, COALESCE(current_artifact_id,''), latest_revision, status, stale_reason, last_error_code, last_error_message, updated_at
		FROM artifact_heads WHERE current_artifact_id IS NOT NULL AND status IN ('current','stale')`)
	if err != nil {
		return nil, apperror.StoreCorrupted(err)
	}
	defer rows.Close()
	var heads []ArtifactHead
	for rows.Next() {
		head, err := scanHead(rows)
		if err != nil {
			return nil, apperror.StoreCorrupted(err)
		}
		heads = append(heads, head)
	}
	return heads, rows.Err()
}

// dependencyHit reports whether an artifact's current version depends on any changed
// key (or has a global snapshot dependency), and the reason code.
func dependencyHit(ctx context.Context, tx *sql.Tx, artifactID ArtifactID, symbols, paths, relations map[string]struct{}) (string, bool) {
	rows, err := tx.QueryContext(ctx, "SELECT dependency_kind, COALESCE(symbol_id,''), COALESCE(path,''), COALESCE(relation_key,'') FROM artifact_dependencies WHERE artifact_id = ?", string(artifactID))
	if err != nil {
		return "", false
	}
	defer rows.Close()
	for rows.Next() {
		var kind, symbolID, path, relationKey string
		if err := rows.Scan(&kind, &symbolID, &path, &relationKey); err != nil {
			return "", false
		}
		switch kind {
		case "snapshot":
			return "snapshot_changed", true
		case "symbol", "occurrence":
			if _, ok := symbols[symbolID]; ok {
				return "symbol_changed", true
			}
		case "file":
			if _, ok := paths[path]; ok {
				return "file_changed", true
			}
		case "relation":
			if _, ok := relations[relationKey]; ok {
				return "relation_changed", true
			}
		}
	}
	return "", false
}

func revalidateDependencies(ctx context.Context, tx *sql.Tx, dependencies []ArtifactDependency) error {
	filePaths := make(map[string]struct{})
	symbolIDs := make(map[string]struct{})
	for _, dependency := range dependencies {
		switch dependency.Kind {
		case "file":
			filePaths[dependency.Path] = struct{}{}
		case "symbol":
			if dependency.SymbolID != "" {
				symbolIDs[dependency.SymbolID] = struct{}{}
			}
		}
	}
	fileHashes, err := loadFileHashes(ctx, tx, sortedSet(filePaths))
	if err != nil {
		return apperror.StoreCorrupted(err)
	}
	existingSymbols, err := loadExistingSymbols(ctx, tx, sortedSet(symbolIDs))
	if err != nil {
		return apperror.StoreCorrupted(err)
	}

	for _, dependency := range dependencies {
		switch dependency.Kind {
		case "file":
			hash, exists := fileHashes[dependency.Path]
			if !exists || (dependency.ContentHash != "" && hash != dependency.ContentHash) {
				return apperror.ArtifactInputSnapshotStale()
			}
		case "symbol":
			if dependency.SymbolID != "" {
				if _, exists := existingSymbols[dependency.SymbolID]; !exists {
					return apperror.ArtifactInputSnapshotStale()
				}
			}
		}
	}
	return nil
}

const dependencyQueryBatchSize = 900

func loadFileHashes(ctx context.Context, tx *sql.Tx, paths []string) (map[string]string, error) {
	hashes := make(map[string]string, len(paths))
	for start := 0; start < len(paths); start += dependencyQueryBatchSize {
		end := min(start+dependencyQueryBatchSize, len(paths))
		rows, err := tx.QueryContext(ctx,
			"SELECT path, content_hash FROM files WHERE path IN ("+sqlPlaceholders(end-start)+")",
			stringArgs(paths[start:end])...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var path, hash string
			if err := rows.Scan(&path, &hash); err != nil {
				rows.Close()
				return nil, err
			}
			hashes[path] = hash
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return hashes, nil
}

func loadExistingSymbols(ctx context.Context, tx *sql.Tx, symbolIDs []string) (map[string]struct{}, error) {
	existing := make(map[string]struct{}, len(symbolIDs))
	for start := 0; start < len(symbolIDs); start += dependencyQueryBatchSize {
		end := min(start+dependencyQueryBatchSize, len(symbolIDs))
		rows, err := tx.QueryContext(ctx,
			"SELECT symbol_id FROM symbol_identities WHERE symbol_id IN ("+sqlPlaceholders(end-start)+")",
			stringArgs(symbolIDs[start:end])...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var symbolID string
			if err := rows.Scan(&symbolID); err != nil {
				rows.Close()
				return nil, err
			}
			existing[symbolID] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return existing, nil
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sqlPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func stringArgs(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func insertDependencies(ctx context.Context, tx *sql.Tx, artifactID ArtifactID, dependencies []ArtifactDependency) error {
	deduped := dedupeDependencies(dependencies)
	for _, dependency := range deduped {
		if err := txExec(ctx, tx, `INSERT INTO artifact_dependencies
			(artifact_id, dependency_kind, symbol_id, occurrence_id, path, relation_key, content_hash)
			VALUES(?,?,?,?,?,?,?)`,
			string(artifactID), dependency.Kind, nullIfEmpty(dependency.SymbolID), nullIfEmpty(dependency.OccurrenceID),
			nullIfEmpty(dependency.Path), nullIfEmpty(dependency.RelationKey), dependency.ContentHash); err != nil {
			return apperror.PersistenceFailed(err)
		}
	}
	return nil
}

func readDependencies(ctx context.Context, q rowQueryer, artifactID ArtifactID) ([]ArtifactDependency, error) {
	rows, err := q.QueryContext(ctx, "SELECT dependency_kind, COALESCE(symbol_id,''), COALESCE(occurrence_id,''), COALESCE(path,''), COALESCE(relation_key,''), content_hash FROM artifact_dependencies WHERE artifact_id = ? ORDER BY canonical_key", string(artifactID))
	if err != nil {
		return nil, apperror.StoreCorrupted(err)
	}
	defer rows.Close()
	var dependencies []ArtifactDependency
	for rows.Next() {
		var dependency ArtifactDependency
		if err := rows.Scan(&dependency.Kind, &dependency.SymbolID, &dependency.OccurrenceID, &dependency.Path, &dependency.RelationKey, &dependency.ContentHash); err != nil {
			return nil, apperror.StoreCorrupted(err)
		}
		dependencies = append(dependencies, dependency)
	}
	return dependencies, rows.Err()
}

func dedupeDependencies(dependencies []ArtifactDependency) []ArtifactDependency {
	seen := map[string]struct{}{}
	out := make([]ArtifactDependency, 0, len(dependencies))
	for _, dependency := range dependencies {
		key := dependency.Kind + "\x1f" + dependency.SymbolID + "\x1f" + dependency.OccurrenceID + "\x1f" + dependency.Path + "\x1f" + dependency.RelationKey
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, dependency)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Kind+out[i].SymbolID+out[i].Path < out[j].Kind+out[j].SymbolID+out[j].Path
	})
	return out
}

func deriveArtifactID(artifactType, key string, revision int, payloadHash string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s", artifactType, key, revision, payloadHash)))
	return "artifact:" + hex.EncodeToString(sum[:16])
}

func toSet(slices ...[]string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, slice := range slices {
		for _, value := range slice {
			if value != "" {
				set[value] = struct{}{}
			}
		}
	}
	return set
}

func bound(value string, limit int) string {
	if len(value) > limit {
		return textutil.TruncateUTF8(value, limit)
	}
	return value
}
