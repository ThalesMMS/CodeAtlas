package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), Config{WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func assertCode(t *testing.T, err error, want apperror.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var appErr *apperror.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error is not an *apperror.AppError: %v", err)
	}
	if appErr.Code != want {
		t.Fatalf("code = %s, want %s", appErr.Code, want)
	}
}

func TestOpenCreatesStateAreaAndSchema(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	db, err := Open(context.Background(), Config{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("Open: %v (cause: %v)", err, errors.Unwrap(err))
	}
	defer db.Close()

	// resolveStatePath canonicalizes via EvalSymlinks (e.g. macOS /var → /private/var).
	resolvedWorkspace, _ := filepath.EvalSymlinks(workspace)
	wantPath := filepath.Join(resolvedWorkspace, StateDirName, DatabaseFileName)
	if db.Path() != wantPath {
		t.Fatalf("Path() = %q, want %q", db.Path(), wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("database file missing: %v", err)
	}
	info, err := os.Stat(filepath.Join(workspace, StateDirName))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("state dir perm = %o, want 700", perm)
		}
	}
	version, err := SchemaVersion(context.Background(), db.Writer())
	if err != nil {
		t.Fatal(err)
	}
	if version != 6 {
		t.Fatalf("schema version = %d, want 6", version)
	}
}

func TestConfiguredPathMustStayInsideWorkspace(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "escaped.db")
	_, err := Open(context.Background(), Config{WorkspaceRoot: workspace, Path: outside})
	assertCode(t, err, apperror.CodeDatabaseOpenFailed)
	if _, statErr := os.Stat(outside); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("configured DB outside workspace was created: stat err=%v", statErr)
	}
}

func TestConfiguredPathInsideWorkspaceOpens(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	configured := filepath.Join(workspace, StateDirName, "custom.db")
	db, err := Open(context.Background(), Config{WorkspaceRoot: workspace, Path: configured})
	if err != nil {
		t.Fatalf("Open configured path inside workspace: %v", err)
	}
	defer db.Close()
	resolvedWorkspace, _ := filepath.EvalSymlinks(workspace)
	wantPath := filepath.Join(resolvedWorkspace, StateDirName, "custom.db")
	if db.Path() != wantPath {
		t.Fatalf("Path() = %q, want %q", db.Path(), wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("configured database missing: %v", err)
	}
}

func TestAllSchemaTablesExist(t *testing.T) {
	t.Parallel()
	db := openTemp(t)
	want := []string{
		"repository_state", "files", "symbol_identities", "symbol_occurrences",
		"relations", "relation_evidence",
		"artifacts", "artifact_dependencies", "schema_migrations", "snapshot_leaves", "snapshot_buckets", "fts_symbol_rows",
	}
	for _, table := range want {
		var name string
		err := db.Reader().QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}
}

// Migration 0006 (issue #163) drops the dense-retrieval tables that 0001 still
// creates, so a freshly migrated database must not carry them: the assertion
// fails if 0006 stops being applied or if the tables are reintroduced.
func TestEmbeddingTablesAreDroppedByMigration(t *testing.T) {
	t.Parallel()
	db := openTemp(t)
	for _, table := range []string{"embeddings", "embedding_index_metadata"} {
		var name string
		err := db.Reader().QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("table %q still present after migration 0006 (scan err = %v)", table, err)
		}
	}
}

// assertTableAbsent fails unless `table` is gone from the schema.
func assertTableAbsent(t *testing.T, db *DB, table string) {
	t.Helper()
	var name string
	err := db.Reader().QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("table %q still present (scan err = %v)", table, err)
	}
}

// TestExistingV5DatabaseUpgradesAndDropsEmbeddings covers the case migration 0006
// actually exists for: a workspace indexed by an older build, sitting at schema 5
// with POPULATED embeddings tables. TestEmbeddingTablesAreDroppedByMigration only
// exercises a fresh database, where 0001 creates the tables and 0006 drops them
// inside the same Open and no rows are ever present — so it would stay green even
// if the DROP could not cope with existing rows or foreign-key references.
//
// A genuine v5 database is reconstructed by re-creating both tables with 0001's
// DDL, seeding a vector that references a real symbol_identities row (exercising
// the ON DELETE CASCADE foreign key), and un-recording version 6. Reopening must
// re-apply 0006, land at 6, and leave unrelated rows untouched.
func TestExistingV5DatabaseUpgradesAndDropsEmbeddings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workspace := t.TempDir()

	db, err := Open(ctx, Config{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seedIdentity(t, db, "sym_kept")
	// Rewind to schema 5: restore 0001's embeddings DDL, populate it, and forget
	// that 0006 was ever applied. Checksums for 1..5 stay valid.
	for _, stmt := range []string{
		// Unconditional so the rewind reaches the v5 shape whether or not 0006 ran,
		// which keeps a missing 0006 failing on the assertion rather than on setup.
		`DROP TABLE IF EXISTS embeddings`,
		`DROP TABLE IF EXISTS embedding_index_metadata`,
		`CREATE TABLE embedding_index_metadata (
  id               INTEGER PRIMARY KEY CHECK (id = 1),
  enabled          INTEGER NOT NULL CHECK (enabled IN (0, 1)),
  provider         TEXT NOT NULL,
  model            TEXT NOT NULL,
  dimension        INTEGER NOT NULL CHECK (dimension >= 0),
  template_version TEXT NOT NULL,
  distance         TEXT NOT NULL,
  encoding         TEXT NOT NULL DEFAULT '',
  built_at         TEXT NOT NULL
)`,
		`CREATE TABLE embeddings (
  symbol_id     TEXT PRIMARY KEY REFERENCES symbol_identities(symbol_id) ON DELETE CASCADE,
  occurrence_id TEXT REFERENCES symbol_occurrences(occurrence_id) ON DELETE SET NULL,
  content_hash  TEXT NOT NULL,
  dimension     INTEGER NOT NULL CHECK (dimension > 0),
  vector        BLOB NOT NULL
)`,
		`INSERT INTO embedding_index_metadata(id,enabled,provider,model,dimension,template_version,distance,encoding,built_at)
  VALUES(1,1,'openai-compatible','default',3,'v1','cosine','float32-le-v1','2026-01-01T00:00:00Z')`,
		`INSERT INTO embeddings(symbol_id,occurrence_id,content_hash,dimension,vector)
  VALUES('sym_kept',NULL,'h1',3,x'000000000000803f0000000000000000')`,
		`DELETE FROM schema_migrations WHERE version=6`,
	} {
		if _, err := db.Writer().ExecContext(ctx, stmt); err != nil {
			t.Fatalf("rewind to v5 (%.40s...): %v", stmt, err)
		}
	}
	if version, _ := SchemaVersion(ctx, db.Writer()); version != 5 {
		t.Fatalf("rewound schema version = %d, want 5", version)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	upgraded, err := Open(ctx, Config{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("reopen of a v5 database must migrate cleanly, got: %v", err)
	}
	defer upgraded.Close()

	version, err := SchemaVersion(ctx, upgraded.Writer())
	if err != nil {
		t.Fatal(err)
	}
	if version != 6 {
		t.Fatalf("schema version after upgrade = %d, want 6", version)
	}
	assertTableAbsent(t, upgraded, "embeddings")
	assertTableAbsent(t, upgraded, "embedding_index_metadata")

	// The upgrade drops the vectors, not the index it was built from.
	var kept int
	if err := upgraded.Reader().QueryRowContext(ctx,
		"SELECT count(*) FROM symbol_identities WHERE symbol_id='sym_kept'").Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("symbol_identities row count = %d, want 1: migration 0006 must not touch the index", kept)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	for i := 0; i < 3; i++ {
		db, err := Open(context.Background(), Config{WorkspaceRoot: workspace})
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		version, _ := SchemaVersion(context.Background(), db.Writer())
		if version != 6 {
			t.Fatalf("open #%d: schema version = %d, want 6", i, version)
		}
		_ = db.Close()
	}
	// Every migration recorded exactly once (idempotent reopen).
	db := openTemp2(t, workspace)
	var count int
	db.Reader().QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count)
	if count != 6 {
		t.Fatalf("schema_migrations has %d rows, want 6", count)
	}
	_ = db.Close()
}

func openTemp2(t *testing.T, workspace string) *DB {
	t.Helper()
	db, err := Open(context.Background(), Config{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	return db
}

func TestChecksumMismatchIsFatal(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	db, err := Open(context.Background(), Config{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	// Tamper with the recorded checksum, then reopen.
	if _, err := db.Writer().Exec("UPDATE schema_migrations SET checksum='deadbeef' WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	_, err = Open(context.Background(), Config{WorkspaceRoot: workspace})
	assertCode(t, err, apperror.CodeDatabaseMigrationChecksumMismatch)
}

func TestVersionTooNewIsFatal(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	db, err := Open(context.Background(), Config{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec("INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(999,'future','x','2026-01-01T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	_, err = Open(context.Background(), Config{WorkspaceRoot: workspace})
	assertCode(t, err, apperror.CodeDatabaseVersionTooNew)
}

func TestForeignKeysEnforced(t *testing.T) {
	t.Parallel()
	db := openTemp(t)
	// An occurrence referencing a non-existent file/symbol must be rejected.
	_, err := db.Writer().Exec(`INSERT INTO symbol_occurrences
		(occurrence_id,symbol_id,file_path,start_line,start_col,end_line,end_col,signature,code,summary,body_hash,file_hash)
		VALUES('occ1','nope','nofile.go',1,1,1,2,'','','','','')`)
	if err == nil {
		t.Fatal("expected a foreign key violation")
	}
}

func TestRelationCanonicalUniquenessWithNulls(t *testing.T) {
	t.Parallel()
	db := openTemp(t)
	seedIdentity(t, db, "subj")
	insert := func() error {
		_, err := db.Writer().Exec(`INSERT INTO relations
			(kind,subject_symbol_id,object_symbol_id,external_key,direction,resolution,ranking_confidence)
			VALUES('calls','subj',NULL,'pkg.ext','out','resolved',0.9)`)
		return err
	}
	if err := insert(); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// Same canonical key (object NULL, same external_key) must collide despite NULLs.
	if err := insert(); err == nil {
		t.Fatal("expected a uniqueness violation on the generated canonical key")
	}
}

func TestRangeConstraints(t *testing.T) {
	t.Parallel()
	db := openTemp(t)
	seedIdentity(t, db, "s1")
	if _, err := db.Writer().Exec("INSERT INTO files(path,language,content_hash,size,modified_at,indexed_at) VALUES('a.go','go','h',1,'t','t')"); err != nil {
		t.Fatal(err)
	}
	// end_line < start_line must be rejected.
	_, err := db.Writer().Exec(`INSERT INTO symbol_occurrences
		(occurrence_id,symbol_id,file_path,start_line,start_col,end_line,end_col,signature,code,summary,body_hash,file_hash)
		VALUES('o1','s1','a.go',5,1,3,1,'','','','','')`)
	if err == nil {
		t.Fatal("expected a CHECK violation for end_line < start_line")
	}
}

func TestCloseLeavesNoGrowingWAL(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	db, err := Open(context.Background(), Config{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	seedIdentity(t, db, "x")
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After a truncating checkpoint on close, the -wal sidecar should be absent or empty.
	walPath := filepath.Join(workspace, StateDirName, DatabaseFileName+"-wal")
	if info, err := os.Stat(walPath); err == nil && info.Size() > 0 {
		t.Fatalf("-wal is %d bytes after close, want absent/empty", info.Size())
	}
}

func TestMigrationChecksumsAreStable(t *testing.T) {
	t.Parallel()
	// Guards against an accidental edit to an applied migration: the embedded set
	// must parse, be contiguous from 1, and 0001 must be the init.
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migrations) == 0 || migrations[0].version != 1 || migrations[0].name != "init" {
		t.Fatalf("unexpected migration set: %+v", migrations)
	}
}

func seedIdentity(t *testing.T, db *DB, symbolID string) {
	t.Helper()
	if _, err := db.Writer().Exec(
		"INSERT INTO symbol_identities(symbol_id,language,kind,name,qualified_name,signature_fingerprint) VALUES(?,?,?,?,?,?)",
		symbolID, "go", "func", symbolID, "pkg."+symbolID, "fp"); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
}

// Ensure the embedded migrations directory is wired (fails loudly if renamed).
var _ = fs.ValidPath("migrations/0001_init.sql")
var _ = sql.LevelDefault
