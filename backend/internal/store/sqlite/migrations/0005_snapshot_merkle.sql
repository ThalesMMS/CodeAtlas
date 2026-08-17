-- Incremental content-addressed SnapshotID index. Canonical structural rows are
-- hashed into deterministic leaves; 256 buckets bound recomputation after a
-- changeset while preserving the same root for identical repository content.
CREATE TABLE snapshot_leaves (
  section    TEXT NOT NULL,
  entity_key TEXT NOT NULL,
  bucket     INTEGER NOT NULL CHECK (bucket >= 0 AND bucket <= 255),
  digest     TEXT NOT NULL CHECK (length(digest) = 64),
  PRIMARY KEY (section, entity_key)
) WITHOUT ROWID;
CREATE INDEX idx_snapshot_leaves_bucket ON snapshot_leaves(bucket, section, entity_key);

CREATE TABLE snapshot_buckets (
  bucket INTEGER PRIMARY KEY CHECK (bucket >= 0 AND bucket <= 255),
  digest TEXT NOT NULL CHECK (length(digest) = 64)
);

-- FTS5 UNINDEXED columns are not lookup indexes. Keep the stable symbol-to-rowid
-- projection so an incremental delete is a primary-key lookup, not an FTS scan.
CREATE TABLE fts_symbol_rows (
  symbol_id TEXT PRIMARY KEY,
  fts_rowid INTEGER NOT NULL UNIQUE
) WITHOUT ROWID;
INSERT INTO fts_symbol_rows(symbol_id, fts_rowid)
  SELECT symbol_id, rowid FROM fts_symbols;

CREATE INDEX idx_evidence_file ON relation_evidence(file_path, relation_id);
