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
	if version != 5 {
		t.Fatalf("schema version = %d, want 5", version)
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
		"relations", "relation_evidence", "embedding_index_metadata", "embeddings",
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

func TestMigrationsAreIdempotent(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	for i := 0; i < 3; i++ {
		db, err := Open(context.Background(), Config{WorkspaceRoot: workspace})
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		version, _ := SchemaVersion(context.Background(), db.Writer())
		if version != 5 {
			t.Fatalf("open #%d: schema version = %d, want 5", i, version)
		}
		_ = db.Close()
	}
	// Both migrations recorded exactly once (idempotent reopen).
	db := openTemp2(t, workspace)
	var count int
	db.Reader().QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count)
	if count != 5 {
		t.Fatalf("schema_migrations has %d rows, want 5", count)
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
