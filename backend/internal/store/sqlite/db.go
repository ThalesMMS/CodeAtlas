// Package sqlite implements the durable SQLite-backed state for CodeAtlas: it
// opens and configures the database per ADR-011 (mattn/go-sqlite3, WAL, one
// serialized writer + a read-only reader pool), and applies the versioned,
// forward-only migrations. It does NOT yet replace the in-memory Store (issue
// #52 only stands up the database and schema v1); FTS5 search tables are deferred
// to #54. All pragmas are read back and verified — executing a PRAGMA is not
// enough; a rejected mandatory setting fails the open.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"

	_ "github.com/mattn/go-sqlite3"
)

const (
	driverName = "sqlite3"
	// StateDirName is the per-workspace state area; the indexer must ignore it.
	StateDirName = ".codeatlas"
	// DatabaseFileName is the database file inside the state area.
	DatabaseFileName = "codeatlas.db"

	defaultBusyTimeout = 5 * time.Second
	readerPoolSize     = 4
)

// Config configures Open. Zero values fall back to the ADR defaults.
type Config struct {
	WorkspaceRoot string
	Path          string
	BusyTimeout   time.Duration
	ReaderPool    int
}

// DB owns the write and read connection pools. The writer is capped at a single
// connection so writes serialize (BEGIN IMMEDIATE); readers run concurrently under
// WAL against a read-only pool.
type DB struct {
	write *sql.DB
	read  *sql.DB
	path  string
}

// Open resolves the state directory under the workspace, opens and verifies the
// database, and applies all migrations. The state dir is created 0700 and must not
// resolve (via symlink) outside the workspace.
func Open(ctx context.Context, config Config) (*DB, error) {
	if config.WorkspaceRoot == "" {
		return nil, apperror.DatabaseOpenFailed(fmt.Errorf("workspace root is required"))
	}
	if config.BusyTimeout <= 0 {
		config.BusyTimeout = defaultBusyTimeout
	}
	if config.ReaderPool <= 0 {
		config.ReaderPool = readerPoolSize
	}

	dbPath, err := resolveStatePath(config.WorkspaceRoot, config.Path)
	if err != nil {
		return nil, err
	}

	write, err := sql.Open(driverName, writeDSN(dbPath, config.BusyTimeout))
	if err != nil {
		return nil, apperror.DatabaseOpenFailed(err)
	}
	// A single writer connection serializes writes and keeps WAL semantics simple.
	write.SetMaxOpenConns(1)
	write.SetConnMaxLifetime(0)
	if err := verifyPragmas(ctx, write, true); err != nil {
		_ = write.Close()
		return nil, err
	}
	if err := migrate(ctx, write); err != nil {
		_ = write.Close()
		return nil, err
	}

	// The reader pool is opened read-only AFTER the writer created the DB + WAL.
	read, err := sql.Open(driverName, readDSN(dbPath, config.BusyTimeout))
	if err != nil {
		_ = write.Close()
		return nil, apperror.DatabaseOpenFailed(err)
	}
	read.SetMaxOpenConns(config.ReaderPool)
	if err := verifyPragmas(ctx, read, false); err != nil {
		_ = write.Close()
		_ = read.Close()
		return nil, err
	}
	return &DB{write: write, read: read, path: dbPath}, nil
}

// Path returns the database file path.
func (db *DB) Path() string { return db.path }

// Writer returns the serialized write pool (max one connection).
func (db *DB) Writer() *sql.DB { return db.write }

// Reader returns the read-only reader pool.
func (db *DB) Reader() *sql.DB { return db.read }

// Close closes both pools, checkpointing the WAL so no -wal/-shm sidecar is left
// growing. Errors from either pool are joined.
func (db *DB) Close() error {
	var firstErr error
	if db.write != nil {
		// Best-effort truncating checkpoint before closing the writer.
		_, _ = db.write.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		if err := db.write.Close(); err != nil {
			firstErr = err
		}
	}
	if db.read != nil {
		if err := db.read.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// resolveStatePath creates the database parent directory with restrictive
// permissions and returns the canonical database path, rejecting configured or
// default locations that escape the workspace via absolute path or symlink.
func resolveStatePath(workspaceRoot, configuredPath string) (string, error) {
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", apperror.DatabaseOpenFailed(err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if configuredPath != "" {
		dbPath, err := filepath.Abs(configuredPath)
		if err != nil {
			return "", apperror.DatabaseOpenFailed(err)
		}
		dbDir, err := canonicalizePath(filepath.Dir(dbPath))
		if err != nil {
			return "", apperror.DatabaseOpenFailed(err)
		}
		if !withinRoot(root, dbDir) {
			return "", apperror.DatabaseOpenFailed(fmt.Errorf("database directory escapes the workspace"))
		}
		if err := os.MkdirAll(dbDir, 0o700); err != nil {
			return "", apperror.DatabaseOpenFailed(err)
		}
		resolvedDir, err := filepath.EvalSymlinks(dbDir)
		if err != nil {
			return "", apperror.DatabaseOpenFailed(err)
		}
		if !withinRoot(root, resolvedDir) {
			return "", apperror.DatabaseOpenFailed(fmt.Errorf("database directory escapes the workspace"))
		}
		resolvedPath := filepath.Join(resolvedDir, filepath.Base(dbPath))
		if realPath, err := filepath.EvalSymlinks(resolvedPath); err == nil {
			if !withinRoot(root, realPath) {
				return "", apperror.DatabaseOpenFailed(fmt.Errorf("database path escapes the workspace"))
			}
			resolvedPath = realPath
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", apperror.DatabaseOpenFailed(err)
		}
		_ = os.Chmod(resolvedDir, 0o700)
		return resolvedPath, nil
	}
	stateDir := filepath.Join(root, StateDirName)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", apperror.DatabaseOpenFailed(err)
	}
	// After creation the real path must still be inside the workspace root.
	resolvedState, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		return "", apperror.DatabaseOpenFailed(err)
	}
	if resolvedState != stateDir || !withinRoot(root, resolvedState) {
		return "", apperror.DatabaseOpenFailed(fmt.Errorf("state directory escapes the workspace"))
	}
	// Tighten perms in case the directory pre-existed with looser bits.
	_ = os.Chmod(stateDir, 0o700)
	return filepath.Join(stateDir, DatabaseFileName), nil
}

func canonicalizePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	existing := abs
	var missing []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", os.ErrNotExist
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	for _, segment := range missing {
		resolved = filepath.Join(resolved, segment)
	}
	return resolved, nil
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// writeDSN applies the mandatory pragmas to every pooled writer connection.
func writeDSN(path string, busy time.Duration) string {
	return fmt.Sprintf("file:%s?_foreign_keys=ON&_busy_timeout=%d&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate",
		path, busy.Milliseconds())
}

// readDSN opens read-only connections (the writer created the DB + WAL first).
func readDSN(path string, busy time.Duration) string {
	return fmt.Sprintf("file:%s?mode=ro&_foreign_keys=ON&_busy_timeout=%d&_query_only=ON",
		path, busy.Milliseconds())
}

// verifyPragmas reads back the effective settings and fails if a mandatory one was
// not accepted. journalMustBeWAL is false for the read-only pool (WAL is a DB-level
// mode established by the writer; readers only need foreign_keys enforced).
func verifyPragmas(ctx context.Context, db *sql.DB, journalMustBeWAL bool) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return apperror.DatabaseOpenFailed(err)
	}
	defer conn.Close()

	var foreignKeys int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return apperror.DatabaseOpenFailed(err)
	}
	if foreignKeys != 1 {
		return apperror.DatabaseOpenFailed(fmt.Errorf("foreign_keys not enforced on the connection"))
	}
	if journalMustBeWAL {
		var journal string
		if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
			return apperror.DatabaseOpenFailed(err)
		}
		if !strings.EqualFold(journal, "wal") {
			return apperror.DatabaseOpenFailed(fmt.Errorf("journal_mode is %q, want WAL", journal))
		}
	}
	return nil
}
