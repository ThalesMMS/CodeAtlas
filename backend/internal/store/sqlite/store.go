package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/symbols"
)

// CommitResult reports the outcome of applying a ChangeSet.
type CommitResult struct {
	ChangeSetID string
	Revision    domain.Revision
	SnapshotID  domain.SnapshotID
	NoOp        bool
}

// RepositoryCounts is the lightweight status projection of the current store.
// It deliberately excludes artifact payloads and symbol/source text.
type RepositoryCounts struct {
	Files     int
	Symbols   int
	Edges     int
	WikiPages int
	IndexedAt time.Time
}

// RepositoryStore is the neutral persistence port the indexer/services depend on.
// The SQLite implementation keeps all SQL inside this package; no connection,
// transaction or query type crosses the boundary.
type RepositoryStore interface {
	Metadata(ctx context.Context) (domain.SnapshotMetadata, error)
	OpenReadView(ctx context.Context) (ReadView, error)
	Commit(ctx context.Context, change *changeset.ChangeSet) (CommitResult, error)
	Validate(ctx context.Context) error
	Close() error
}

// ErrVersionConflict is returned when a ChangeSet's expected revision does not
// match the store's current revision (optimistic concurrency).
var ErrVersionConflict = errors.New("sqlite store: revision conflict")

// Store is the SQLite-backed RepositoryStore. Structural reads/writes are
// implemented over schema v1; FTS5 search (#54) and artifacts (#56) are explicit
// incomplete adapters that never fake success.
type Store struct {
	db                *DB
	maxReadViewWindow time.Duration
}

var _ RepositoryStore = (*Store)(nil)

// OpenStore opens (creating/migrating) the database and seeds the structural
// singleton, returning a ready store at revision 0 for a fresh database.
func OpenStore(ctx context.Context, config Config) (*Store, error) {
	db, err := Open(ctx, config)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, maxReadViewWindow: defaultReadViewWindow}
	if err := store.ensureRepositoryState(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases both connection pools.
func (s *Store) Close() error { return s.db.Close() }

// ensureRepositoryState inserts the singleton row for a fresh database (revision 0,
// empty content snapshot). It is a no-op once present.
func (s *Store) ensureRepositoryState(ctx context.Context) error {
	var exists int
	if err := s.db.Writer().QueryRowContext(ctx, "SELECT COUNT(*) FROM repository_state WHERE id = 1").Scan(&exists); err != nil {
		return apperror.DatabaseOpenFailed(err)
	}
	if exists > 0 {
		return s.ensureSnapshotSchema(ctx)
	}
	snapshotID, err := computeSnapshotID(ctx, s.db.Writer())
	if err != nil {
		return apperror.DatabaseOpenFailed(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Writer().ExecContext(ctx, `INSERT INTO repository_state
		(id, revision, snapshot_id, snapshot_schema, identity_algorithm_version, parser_metadata, created_at, indexed_at)
		VALUES (1, 0, ?, ?, ?, ?, ?, ?)`,
		string(snapshotID), snapshotSchema, symbols.IdentityAlgorithmVersion, `{"docComments":"v1"}`, now, now)
	if err != nil {
		return apperror.DatabaseOpenFailed(err)
	}
	return nil
}

// ensureSnapshotSchema invalidates structural rows when their canonical meaning
// predates this build (for example, snapshots indexed before doc comments became
// occurrence evidence). Leaving file hashes in place would make the indexer see
// a false no-op, so the reset is atomic. Immutable artifacts are preserved but
// their heads are marked stale until regenerated against the rebuilt index.
func (s *Store) ensureSnapshotSchema(ctx context.Context) error {
	tx, err := s.db.Writer().BeginTx(ctx, nil)
	if err != nil {
		return apperror.DatabaseOpenFailed(err)
	}
	defer tx.Rollback() //nolint:errcheck

	var revision int64
	var storedSchema int
	if err := tx.QueryRowContext(ctx, "SELECT revision, snapshot_schema FROM repository_state WHERE id = 1").Scan(&revision, &storedSchema); err != nil {
		return apperror.DatabaseOpenFailed(err)
	}
	if storedSchema == snapshotSchema {
		return nil
	}
	if storedSchema > snapshotSchema {
		return apperror.StoreCorrupted(fmt.Errorf("repository snapshot schema %d is newer than supported %d", storedSchema, snapshotSchema))
	}

	for _, table := range []string{
		"relation_evidence", "relations", "symbol_occurrences",
		"symbol_identities", "files", "fts_symbols", "fts_symbol_rows", "fts_metadata",
	} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return apperror.DatabaseOpenFailed(err)
		}
	}
	snapshotID, err := computeSnapshotID(ctx, tx)
	if err != nil {
		return apperror.DatabaseOpenFailed(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE artifact_heads SET status = 'stale',
		stale_reason = 'repository snapshot schema changed', updated_at = ?`, now); err != nil {
		return apperror.DatabaseOpenFailed(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repository_state
		SET revision = ?, snapshot_id = ?, snapshot_schema = ?, parser_metadata = ?, indexed_at = ? WHERE id = 1`,
		revision+1, string(snapshotID), snapshotSchema, `{"docComments":"v1"}`, now); err != nil {
		return apperror.DatabaseOpenFailed(err)
	}
	if err := tx.Commit(); err != nil {
		return apperror.DatabaseOpenFailed(err)
	}
	return nil
}

// Metadata returns the current structural snapshot metadata from the singleton.
func (s *Store) Metadata(ctx context.Context) (domain.SnapshotMetadata, error) {
	return readMetadata(ctx, s.db.Reader())
}

// FileHash performs an indexed point lookup without materializing a ReadView.
func (s *Store) FileHash(ctx context.Context, path string) (string, bool, error) {
	var hash string
	err := s.db.Reader().QueryRowContext(ctx, "SELECT content_hash FROM files WHERE path = ?", path).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, apperror.StoreCorrupted(err)
	}
	return hash, true, nil
}

// Counts reads status counters with scalar SQL aggregates. Edges intentionally
// count relation-evidence rows, matching the one-domain-edge-per-evidence shape
// exposed by ReadView.AllEdges. Wiki pages count current/stale published heads
// without loading or decoding their full payloads.
func (s *Store) Counts(ctx context.Context) (RepositoryCounts, error) {
	var counts RepositoryCounts
	var indexedRaw string
	err := s.db.Reader().QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM files),
		(SELECT COUNT(*) FROM symbol_identities),
		(SELECT COUNT(*) FROM relation_evidence),
		(SELECT COUNT(*) FROM artifact_heads
			WHERE artifact_type = 'deepwiki'
			AND current_artifact_id IS NOT NULL
			AND status IN ('current','stale')),
		indexed_at
		FROM repository_state WHERE id = 1`).Scan(
		&counts.Files, &counts.Symbols, &counts.Edges, &counts.WikiPages, &indexedRaw,
	)
	if err != nil {
		return RepositoryCounts{}, apperror.StoreCorrupted(err)
	}
	counts.IndexedAt, _ = time.Parse(time.RFC3339Nano, indexedRaw)
	return counts, nil
}

// KnownPaths returns only the sorted file-path projection, without materializing
// a full structural ReadView.
func (s *Store) KnownPaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.Reader().QueryContext(ctx, "SELECT path FROM files ORDER BY path")
	if err != nil {
		return nil, apperror.StoreCorrupted(err)
	}
	defer rows.Close()
	paths := make([]string, 0)
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, apperror.StoreCorrupted(err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, apperror.StoreCorrupted(err)
	}
	return paths, nil
}

func readMetadata(ctx context.Context, q rowQueryer) (domain.SnapshotMetadata, error) {
	row := q.QueryContext
	rs, err := row(ctx, "SELECT revision, snapshot_id, snapshot_schema, identity_algorithm_version, indexed_at FROM repository_state WHERE id = 1")
	if err != nil {
		return domain.SnapshotMetadata{}, apperror.DatabaseOpenFailed(err)
	}
	defer rs.Close()
	if !rs.Next() {
		if err := rs.Err(); err != nil {
			return domain.SnapshotMetadata{}, apperror.DatabaseOpenFailed(err)
		}
		return domain.SnapshotMetadata{}, apperror.StoreCorrupted(fmt.Errorf("repository_state singleton missing"))
	}
	var (
		revision   int64
		snapshotID string
		schema     int
		algorithm  string
		createdRaw string
	)
	if err := rs.Scan(&revision, &snapshotID, &schema, &algorithm, &createdRaw); err != nil {
		return domain.SnapshotMetadata{}, apperror.StoreCorrupted(err)
	}
	createdAt, _ := time.Parse(time.RFC3339Nano, createdRaw)
	return domain.SnapshotMetadata{
		ID:                domain.SnapshotID(snapshotID),
		Revision:          domain.Revision(revision),
		CreatedAt:         createdAt,
		Schema:            schema,
		IdentityAlgorithm: algorithm,
	}, nil
}

// Validate audits structural integrity (foreign keys, the singleton, and the
// SQLite page integrity), returning a StoreCorrupted error on any violation.
func (s *Store) Validate(ctx context.Context) error {
	// foreign_key_check returns one row per violation.
	rows, err := s.db.Writer().QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return apperror.StoreCorrupted(err)
	}
	violation := rows.Next()
	_ = rows.Close()
	if violation {
		return apperror.StoreCorrupted(fmt.Errorf("foreign key violations present"))
	}

	var integrity string
	if err := s.db.Writer().QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&integrity); err != nil {
		return apperror.StoreCorrupted(err)
	}
	if integrity != "ok" {
		return apperror.StoreCorrupted(fmt.Errorf("integrity_check: %s", integrity))
	}

	var singleton int
	if err := s.db.Writer().QueryRowContext(ctx, "SELECT COUNT(*) FROM repository_state").Scan(&singleton); err != nil {
		return apperror.StoreCorrupted(err)
	}
	if singleton != 1 {
		return apperror.StoreCorrupted(fmt.Errorf("repository_state has %d rows, want 1", singleton))
	}
	return nil
}

// txExec is a tiny helper to keep commit statements readable.
func txExec(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}
