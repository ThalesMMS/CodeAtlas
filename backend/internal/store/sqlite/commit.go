package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/storederive"
)

// Commit applies a validated ChangeSet atomically in a single IMMEDIATE write
// transaction: it checks the expected revision, applies deletes and upserts (with
// derived identities/occurrences/relations), recomputes the content-addressed
// SnapshotID and bumps the revision. Nothing is visible until the transaction
// commits; on any error the prior state is intact.
func (s *Store) Commit(ctx context.Context, change *changeset.ChangeSet) (CommitResult, error) {
	if change == nil {
		return CommitResult{}, apperror.InvalidArgument("changeset is nil", nil)
	}
	if change.HasErrors() {
		return CommitResult{}, apperror.InvalidArgument("changeset has blocking diagnostics", nil)
	}

	tx, err := s.db.Writer().BeginTx(ctx, nil) // writer DSN sets _txlock=immediate
	if err != nil {
		return CommitResult{}, apperror.PersistenceFailed(err)
	}
	defer tx.Rollback() //nolint:errcheck

	var currentRevision int64
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM repository_state WHERE id = 1").Scan(&currentRevision); err != nil {
		return CommitResult{}, apperror.StoreCorrupted(err)
	}
	if change.ExpectedVersion() != uint64(currentRevision) {
		return CommitResult{}, apperror.StoreVersionConflict()
	}

	metadataResult := func(revision int64, snapshotID domain.SnapshotID, noop bool) CommitResult {
		return CommitResult{ChangeSetID: change.ID(), Revision: domain.Revision(revision), SnapshotID: snapshotID, NoOp: noop}
	}

	if change.IsNoop() {
		var snapshotID string
		if err := tx.QueryRowContext(ctx, "SELECT snapshot_id FROM repository_state WHERE id = 1").Scan(&snapshotID); err != nil {
			return CommitResult{}, apperror.StoreCorrupted(err)
		}
		return metadataResult(currentRevision, domain.SnapshotID(snapshotID), true), nil
	}
	touchedPaths := append(change.DeletedPaths(), upsertPaths(change.Upserts())...)
	before, err := collectCommitImpact(ctx, tx, touchedPaths)
	if err != nil {
		return CommitResult{}, apperror.PersistenceFailed(err)
	}

	for _, path := range change.DeletedPaths() {
		if err := txExec(ctx, tx, "DELETE FROM relation_evidence WHERE file_path = ?", path); err != nil {
			return CommitResult{}, apperror.PersistenceFailed(err)
		}
		if err := txExec(ctx, tx, "DELETE FROM files WHERE path = ?", path); err != nil {
			return CommitResult{}, apperror.PersistenceFailed(err)
		}
	}
	for _, parsed := range change.Upserts() {
		if err := applyUpsert(ctx, tx, parsed); err != nil {
			return CommitResult{}, err
		}
	}
	if err := garbageCollectImpacted(ctx, tx, before); err != nil {
		return CommitResult{}, err
	}
	after, err := collectCommitImpact(ctx, tx, touchedPaths)
	if err != nil {
		return CommitResult{}, apperror.PersistenceFailed(err)
	}
	impact := before.merge(after)
	if err := refreshFTS(ctx, tx, impact.symbolIDs); err != nil {
		return CommitResult{}, apperror.PersistenceFailed(err)
	}

	snapshotID, err := refreshSnapshotID(ctx, tx, impact)
	if err != nil {
		return CommitResult{}, apperror.PersistenceFailed(err)
	}
	nextRevision := currentRevision + 1
	if err := txExec(ctx, tx,
		"UPDATE repository_state SET revision = ?, snapshot_id = ?, indexed_at = ? WHERE id = 1",
		nextRevision, string(snapshotID), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return CommitResult{}, apperror.PersistenceFailed(err)
	}
	if err := tx.Commit(); err != nil {
		return CommitResult{}, apperror.PersistenceFailed(err)
	}
	return metadataResult(nextRevision, snapshotID, false), nil
}

type commitImpact struct {
	paths         map[string]struct{}
	occurrenceIDs map[string]struct{}
	symbolIDs     map[string]struct{}
	relationIDs   map[int64]struct{}
	relationKeys  map[string]struct{}
}

func newCommitImpact(paths []string) commitImpact {
	impact := commitImpact{
		paths: map[string]struct{}{}, occurrenceIDs: map[string]struct{}{},
		symbolIDs: map[string]struct{}{}, relationIDs: map[int64]struct{}{}, relationKeys: map[string]struct{}{},
	}
	for _, path := range paths {
		impact.paths[path] = struct{}{}
	}
	return impact
}

func (i commitImpact) merge(other commitImpact) commitImpact {
	for value := range other.paths {
		i.paths[value] = struct{}{}
	}
	for value := range other.occurrenceIDs {
		i.occurrenceIDs[value] = struct{}{}
	}
	for value := range other.symbolIDs {
		i.symbolIDs[value] = struct{}{}
	}
	for value := range other.relationIDs {
		i.relationIDs[value] = struct{}{}
	}
	for value := range other.relationKeys {
		i.relationKeys[value] = struct{}{}
	}
	return i
}

func upsertPaths(files []domain.ParsedFile) []string {
	paths := make([]string, 0, len(files))
	for _, parsed := range files {
		paths = append(paths, parsed.File.Path)
	}
	return paths
}

// impactRelationsQuery lists the relations touched by one file path: those with
// evidence in the file, plus those whose subject or object symbol occurs in it.
// Every branch is served by an index (idx_evidence_file, idx_occ_file_range,
// idx_relations_subject, idx_relations_object); it must never scan relations.
const impactRelationsQuery = `SELECT r.relation_id, r.canonical_key
	FROM relation_evidence e JOIN relations r ON r.relation_id = e.relation_id
	WHERE e.file_path = ?
UNION SELECT r.relation_id, r.canonical_key
	FROM symbol_occurrences o JOIN relations r ON r.subject_symbol_id = o.symbol_id
	WHERE o.file_path = ?
UNION SELECT r.relation_id, r.canonical_key
	FROM symbol_occurrences o JOIN relations r ON r.object_symbol_id = o.symbol_id
	WHERE o.file_path = ?`

func collectCommitImpact(ctx context.Context, tx *sql.Tx, paths []string) (commitImpact, error) {
	impact := newCommitImpact(paths)
	for _, path := range paths {
		rows, err := tx.QueryContext(ctx, "SELECT occurrence_id, symbol_id FROM symbol_occurrences WHERE file_path = ?", path)
		if err != nil {
			return impact, err
		}
		for rows.Next() {
			var occurrenceID, symbolID string
			if err := rows.Scan(&occurrenceID, &symbolID); err != nil {
				rows.Close()
				return impact, err
			}
			impact.occurrenceIDs[occurrenceID] = struct{}{}
			impact.symbolIDs[symbolID] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return impact, err
		}
		// Three index-driven lookups joined by UNION. The equivalent single query
		// with OR across the evidence join and two IN subqueries made SQLite scan
		// the whole relations table once per touched path, which turned a large
		// initial commit into hours of CPU (see impactRelationsQuery plan test).
		relations, err := tx.QueryContext(ctx, impactRelationsQuery, path, path, path)
		if err != nil {
			return impact, err
		}
		for relations.Next() {
			var relationID int64
			var key string
			if err := relations.Scan(&relationID, &key); err != nil {
				relations.Close()
				return impact, err
			}
			impact.relationIDs[relationID] = struct{}{}
			impact.relationKeys[key] = struct{}{}
		}
		if err := relations.Close(); err != nil {
			return impact, err
		}
	}
	// Deleting an identity can successively null parent_symbol_id throughout an
	// orphaned hierarchy. Include every descendant so its canonical snapshot leaf
	// is refreshed if an ancestor is removed.
	parents := make([]string, 0, len(impact.symbolIDs))
	for symbolID := range impact.symbolIDs {
		parents = append(parents, symbolID)
	}
	for next := 0; next < len(parents); next++ {
		parentID := parents[next]
		rows, err := tx.QueryContext(ctx, "SELECT symbol_id FROM symbol_identities WHERE parent_symbol_id = ?", parentID)
		if err != nil {
			return impact, err
		}
		for rows.Next() {
			var childID string
			if err := rows.Scan(&childID); err != nil {
				rows.Close()
				return impact, err
			}
			if _, seen := impact.symbolIDs[childID]; seen {
				continue
			}
			impact.symbolIDs[childID] = struct{}{}
			parents = append(parents, childID)
		}
		if err := rows.Close(); err != nil {
			return impact, err
		}
	}
	return impact, nil
}

// applyUpsert replaces one file's row, occurrences, identities and relation
// evidence with the freshly resolved content.
func applyUpsert(ctx context.Context, tx *sql.Tx, parsed domain.ParsedFile) error {
	// Remove the file's prior occurrences and the evidence anchored in it; the
	// relations themselves are GC'd later if no evidence remains.
	if err := txExec(ctx, tx, "DELETE FROM symbol_occurrences WHERE file_path = ?", parsed.File.Path); err != nil {
		return apperror.PersistenceFailed(err)
	}
	if err := txExec(ctx, tx, "DELETE FROM relation_evidence WHERE file_path = ?", parsed.File.Path); err != nil {
		return apperror.PersistenceFailed(err)
	}

	file := parsed.File
	now := time.Now().UTC().Format(time.RFC3339Nano)
	modified := file.ModifiedAt
	if modified.IsZero() {
		modified = time.Now().UTC()
	}
	contentTruncated := 0
	if file.ContentTruncated {
		contentTruncated = 1
	}
	if err := txExec(ctx, tx, `INSERT INTO files(path, language, content_hash, size, modified_at, indexed_at, content, content_truncated)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET language=excluded.language, content_hash=excluded.content_hash,
			size=excluded.size, modified_at=excluded.modified_at, indexed_at=excluded.indexed_at,
			content=excluded.content, content_truncated=excluded.content_truncated`,
		file.Path, nonEmpty(file.Language, "unknown"), nonEmpty(file.Hash, "0"), file.Size,
		modified.UTC().Format(time.RFC3339Nano), now, file.Content, contentTruncated); err != nil {
		return apperror.PersistenceFailed(err)
	}

	resolved, handleByLegacy := storederive.ResolveParsedFile(parsed)
	for _, rs := range resolved {
		if err := upsertResolved(ctx, tx, rs); err != nil {
			return err
		}
	}
	for _, edge := range parsed.Edges {
		if err := upsertEdge(ctx, tx, storederive.RemapEdge(edge, handleByLegacy)); err != nil {
			return err
		}
	}
	return nil
}

// upsertResolved writes a symbol's identity (logical or occurrence-only synthetic)
// and its occurrence row.
func upsertResolved(ctx context.Context, tx *sql.Tx, rs domain.ResolvedSymbol) error {
	handle := storederive.SymbolHandle(rs)
	identity := rs.Identity
	occurrenceOnly := 0
	if rs.Occurrence.SymbolID == "" {
		occurrenceOnly = 1
	}
	var parent any
	if identity.ParentID != "" {
		parent = string(identity.ParentID)
	}
	// The synthetic identity for an occurrence-only symbol is keyed by the handle.
	if err := txExec(ctx, tx, `INSERT INTO symbol_identities
		(symbol_id, language, kind, name, qualified_name, parent_symbol_id, signature_fingerprint, occurrence_only)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(symbol_id) DO UPDATE SET language=excluded.language, kind=excluded.kind,
			name=excluded.name, qualified_name=excluded.qualified_name, signature_fingerprint=excluded.signature_fingerprint`,
		handle, nonEmpty(identity.Language, "unknown"), nonEmpty(identity.Kind, "unknown"),
		identity.Name, identity.QualifiedName, parent, identity.SignatureFingerprint, occurrenceOnly); err != nil {
		return apperror.PersistenceFailed(err)
	}

	occ := rs.Occurrence
	if err := txExec(ctx, tx, `INSERT INTO symbol_occurrences
		(occurrence_id, symbol_id, file_path, start_line, start_col, end_line, end_col, signature, code, doc_comment, summary, body_hash, file_hash)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(occurrence_id) DO UPDATE SET symbol_id=excluded.symbol_id, file_path=excluded.file_path,
			start_line=excluded.start_line, start_col=excluded.start_col, end_line=excluded.end_line, end_col=excluded.end_col,
			signature=excluded.signature, code=excluded.code, doc_comment=excluded.doc_comment, summary=excluded.summary,
			body_hash=excluded.body_hash, file_hash=excluded.file_hash`,
		string(occ.ID), handle, occ.Path,
		atLeast(occ.Range.Start.Line, 1), atLeast(occ.Range.Start.Column, 1),
		atLeast(occ.Range.End.Line, atLeast(occ.Range.Start.Line, 1)), atLeast(occ.Range.End.Column, 1),
		occ.Signature, occ.Code, occ.DocComment, occ.Summary, nonEmpty(occ.BodyHash, "0"), occ.FileHash); err != nil {
		return apperror.PersistenceFailed(err)
	}
	return nil
}

// upsertEdge folds a flat edge into the normalized relation + evidence model: a
// logical relation deduped by its canonical key, plus one evidence row per call
// site (the edge's path/line). References to unknown symbols are dropped (the
// subject/object must exist as identities for the FKs to hold).
func upsertEdge(ctx context.Context, tx *sql.Tx, edge domain.Edge) error {
	if edge.FromSymbolID == "" || edge.Type == "" {
		return nil
	}
	if !identityExists(ctx, tx, edge.FromSymbolID) {
		return nil
	}
	var object any
	var external any
	resolution := "unresolved"
	if edge.ToSymbolID != "" && identityExists(ctx, tx, edge.ToSymbolID) {
		object = edge.ToSymbolID
		resolution = "resolved"
	} else if edge.ToName != "" {
		external = edge.ToName
		resolution = "external"
	} else {
		// Nothing to point at and no name: not a storable relation.
		return nil
	}
	confidence := edge.Confidence
	if confidence < 0 {
		confidence = 0
	} else if confidence > 1 {
		confidence = 1
	}

	var relationID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO relations
		(kind, subject_symbol_id, object_symbol_id, external_key, direction, resolution, ranking_confidence)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(canonical_key) DO UPDATE SET ranking_confidence=MAX(relations.ranking_confidence, excluded.ranking_confidence)
		RETURNING relation_id`,
		edge.Type, edge.FromSymbolID, object, external, "out", resolution, confidence).Scan(&relationID); err != nil {
		return apperror.PersistenceFailed(err)
	}

	detail := fmt.Sprintf("%s->%s@%s:%d", edge.FromSymbolID, edge.ToName, edge.Path, edge.Line)
	detailSum := sha256.Sum256([]byte(detail))
	evidenceID := relationEvidenceID(relationID, edge)
	line := atLeast(edge.Line, 0)
	if err := txExec(ctx, tx, `INSERT INTO relation_evidence
		(evidence_id, relation_id, file_path, start_line, start_col, end_line, end_col, source, provider, tool_version, method, confidence, version_known, detail, detail_hash)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(evidence_id) DO NOTHING`,
		evidenceID, relationID, nullIfEmpty(edge.Path), line, 0, line, 0,
		"ast", "indexer", "1", "parse", confidence, 0, detail, hex.EncodeToString(detailSum[:])); err != nil {
		return apperror.PersistenceFailed(err)
	}
	return nil
}

// garbageCollect removes relations with no remaining evidence and identities with
// no remaining occurrences (orphaned by deletes/upserts).
func garbageCollectImpacted(ctx context.Context, tx *sql.Tx, impact commitImpact) error {
	for relationID := range impact.relationIDs {
		if err := txExec(ctx, tx, "DELETE FROM relations WHERE relation_id = ? AND NOT EXISTS (SELECT 1 FROM relation_evidence WHERE relation_id = ?)", relationID, relationID); err != nil {
			return apperror.PersistenceFailed(err)
		}
	}
	for symbolID := range impact.symbolIDs {
		if err := txExec(ctx, tx, "DELETE FROM symbol_identities WHERE symbol_id = ? AND NOT EXISTS (SELECT 1 FROM symbol_occurrences WHERE symbol_id = ?)", symbolID, symbolID); err != nil {
			return apperror.PersistenceFailed(err)
		}
	}
	return nil
}

func identityExists(ctx context.Context, tx *sql.Tx, symbolID string) bool {
	var one int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM symbol_identities WHERE symbol_id = ?", symbolID).Scan(&one)
	return err == nil
}

func relationEvidenceID(relationID int64, edge domain.Edge) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%d", relationID, edge.ToName, edge.Path, edge.Line)))
	return hex.EncodeToString(sum[:16])
}

func nonEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func atLeast(value, minimum int) int {
	if value < minimum {
		return minimum
	}
	return value
}
