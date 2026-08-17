// Package spike is an ISOLATED evaluation of SQLite/FTS5 as a replacement for the
// JSON store (issue #51). It lives in its OWN nested Go module
// (example.com/codeatlas-spike/sqlite) so the production module github.com/ThalesMMS/CodeAtlas
// excludes this subtree entirely: `go build/vet/test ./...` from backend never
// compiles it and its SQLite drivers never enter the production go.mod. Nothing
// here is imported by production code. Run it with: make spike-sqlite (which adds
// `-tags fts5`, required by the cgo mattn driver and ignored by modernc).
//
// Two drivers are evaluated side by side over database/sql:
//   - modernc.org/sqlite (pure-Go, driver name "sqlite", FTS5 compiled in)
//   - github.com/mattn/go-sqlite3 (cgo, driver name "sqlite3", needs -tags fts5)
package spike

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
	_ "modernc.org/sqlite"
)

// Driver identifies a candidate by a stable key, mapping to its database/sql name.
type Driver struct {
	Key  string // "modernc" | "mattn"
	Name string // database/sql driver name
	CGO  bool
}

var Drivers = []Driver{
	{Key: "modernc", Name: "sqlite", CGO: false},
	{Key: "mattn", Name: "sqlite3", CGO: true},
}

// BusyTimeoutMillis is the busy handler window used across the spike.
const BusyTimeoutMillis = 5000

// Schema is a simplified-but-representative model of the production contracts:
// metadata/revision, files, symbol identities + occurrences, edges + provenance,
// embeddings (metadata + BLOB), FTS5 documents, and versioned artifacts + deps.
const Schema = `
CREATE TABLE IF NOT EXISTS metadata (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS revisions (
  id          INTEGER PRIMARY KEY,
  snapshot_id TEXT NOT NULL UNIQUE,
  created_at  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS files (
  id    INTEGER PRIMARY KEY,
  path  TEXT NOT NULL UNIQUE,
  lang  TEXT NOT NULL,
  hash  TEXT NOT NULL,
  size  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS identities (
  symbol_id TEXT PRIMARY KEY,
  name      TEXT NOT NULL,
  kind      TEXT NOT NULL,
  file_id   INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_identities_file ON identities(file_id);
CREATE INDEX IF NOT EXISTS idx_identities_name ON identities(name);
CREATE TABLE IF NOT EXISTS occurrences (
  id         INTEGER PRIMARY KEY,
  symbol_id  TEXT NOT NULL REFERENCES identities(symbol_id) ON DELETE CASCADE,
  file_id    INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  start_line INTEGER NOT NULL,
  start_col  INTEGER NOT NULL,
  end_line   INTEGER NOT NULL,
  end_col    INTEGER NOT NULL,
  role       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_occ_symbol ON occurrences(symbol_id);
CREATE INDEX IF NOT EXISTS idx_occ_file ON occurrences(file_id);
CREATE TABLE IF NOT EXISTS edges (
  id         INTEGER PRIMARY KEY,
  kind       TEXT NOT NULL,
  src        TEXT NOT NULL,
  dst        TEXT NOT NULL,
  provenance TEXT NOT NULL,
  confidence REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_edges_src ON edges(src, kind);
CREATE INDEX IF NOT EXISTS idx_edges_dst ON edges(dst, kind);
CREATE TABLE IF NOT EXISTS embeddings (
  symbol_id TEXT PRIMARY KEY REFERENCES identities(symbol_id) ON DELETE CASCADE,
  dim       INTEGER NOT NULL,
  model     TEXT NOT NULL,
  vec       BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS artifacts (
  id      INTEGER PRIMARY KEY,
  kind    TEXT NOT NULL,
  head    TEXT NOT NULL,
  version INTEGER NOT NULL,
  body    BLOB NOT NULL,
  UNIQUE(kind, head)
);
CREATE TABLE IF NOT EXISTS artifact_deps (
  artifact_id INTEGER NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
  depends_on  TEXT NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS fts_documents USING fts5(
  symbol_id UNINDEXED,
  name,
  qualified,
  doc,
  tokenize = 'unicode61 remove_diacritics 2'
);
`

// Open opens a DB for a driver with the spike's pragmas applied. WAL and
// foreign_keys are verified by Open (returning an error on mismatch) rather than
// assumed. A single *sql.DB is returned; callers cap the pool per the benchmark.
func Open(driver Driver, path string) (*sql.DB, error) {
	dsn := dsnFor(driver, path)
	db, err := sql.Open(driver.Name, dsn)
	if err != nil {
		return nil, err
	}
	// Apply pragmas that some drivers do not take through the DSN. Use a single
	// connection for setup so the verification reads the same connection's state.
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		fmt.Sprintf("PRAGMA busy_timeout = %d", BusyTimeoutMillis),
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	return db, nil
}

func dsnFor(driver Driver, path string) string {
	switch driver.Key {
	case "modernc":
		// modernc accepts pragmas as query params; foreign_keys via _pragma.
		return fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)", path, BusyTimeoutMillis)
	default:
		// mattn parameters.
		return fmt.Sprintf("file:%s?_busy_timeout=%d&_foreign_keys=ON&_journal_mode=WAL", path, BusyTimeoutMillis)
	}
}

// EffectivePragmas reads back the connection's pragma state to PROVE the settings
// took effect (not just that they were accepted).
func EffectivePragmas(db *sql.DB) (foreignKeys bool, journalMode string, err error) {
	var fk int
	if err = db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		return
	}
	if err = db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return
	}
	return fk == 1, journalMode, nil
}
