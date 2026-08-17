package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migration is one forward-only step. Checksum is the SHA-256 of the canonical
// (LF-normalized) content; a divergence on an already-applied version is fatal.
type migration struct {
	version  int
	name     string
	content  string
	checksum string
}

// loadMigrations parses the embedded migrations/NNNN_description.sql files into an
// ascending, gap-checked list. Determinism comes from the version prefix.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(entry.Name())
		if err != nil {
			return nil, err
		}
		raw, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, err
		}
		content := strings.ReplaceAll(string(raw), "\r\n", "\n")
		sum := sha256.Sum256([]byte(content))
		migrations = append(migrations, migration{version: version, name: name, content: content, checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	for i, m := range migrations {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions must be contiguous from 1: got %d at position %d", m.version, i+1)
		}
	}
	return migrations, nil
}

func parseMigrationName(filename string) (int, string, error) {
	base := strings.TrimSuffix(filename, ".sql")
	underscore := strings.IndexByte(base, '_')
	if underscore <= 0 {
		return 0, "", fmt.Errorf("migration %q must be named NNNN_description.sql", filename)
	}
	version, err := strconv.Atoi(base[:underscore])
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf("migration %q has an invalid version prefix", filename)
	}
	return version, base[underscore+1:], nil
}

// migrate ensures schema_migrations exists, then applies each pending migration in
// its own IMMEDIATE transaction (the writer DSN sets _txlock=immediate, so two
// instances cannot apply the same migration concurrently — the loser waits on the
// write lock and then sees the migration as already applied). A recorded checksum
// mismatch or a DB newer than this build is fatal.
func migrate(ctx context.Context, db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return apperror.DatabaseMigrationFailed(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  checksum   TEXT NOT NULL,
  applied_at TEXT NOT NULL
)`); err != nil {
		return apperror.DatabaseMigrationFailed(err)
	}

	maxKnown := 0
	if len(migrations) > 0 {
		maxKnown = migrations[len(migrations)-1].version
	}
	var dbMax sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&dbMax); err != nil {
		return apperror.DatabaseMigrationFailed(err)
	}
	if dbMax.Valid && dbMax.Int64 > int64(maxKnown) {
		return apperror.DatabaseVersionTooNew(map[string]any{"database_version": dbMax.Int64, "supported_version": maxKnown})
	}

	for _, m := range migrations {
		if err := applyOne(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

// applyOne runs a single migration atomically: inside one IMMEDIATE transaction it
// re-checks whether the version is applied (verifying the checksum if so) and, if
// not, executes the SQL and records it. The check-and-apply share the transaction
// so a concurrent instance cannot double-apply.
func applyOne(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return apperror.DatabaseMigrationFailed(err)
	}
	defer tx.Rollback() //nolint:errcheck

	var storedChecksum string
	err = tx.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version = ?", m.version).Scan(&storedChecksum)
	switch {
	case err == nil:
		// Already applied: the content must be byte-identical to what was recorded.
		if storedChecksum != m.checksum {
			return apperror.DatabaseMigrationChecksumMismatch(map[string]any{
				"version": m.version, "name": m.name,
			})
		}
		return nil
	case errors.Is(err, sql.ErrNoRows):
		// Pending: apply and record within this transaction.
	default:
		return apperror.DatabaseMigrationFailed(err)
	}

	if _, err := tx.ExecContext(ctx, m.content); err != nil {
		return apperror.DatabaseMigrationFailed(fmt.Errorf("migration %04d_%s: %w", m.version, m.name, err))
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES(?, ?, ?, ?)",
		m.version, m.name, m.checksum, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return apperror.DatabaseMigrationFailed(err)
	}
	if err := tx.Commit(); err != nil {
		return apperror.DatabaseMigrationFailed(err)
	}
	return nil
}

// SchemaVersion returns the highest applied migration version (0 if none).
func SchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		return 0, err
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}
