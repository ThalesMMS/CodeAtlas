-- Schema v1 for the SQLite store (issue #52). Models identities, occurrences,
-- relations + evidence, embeddings and artifacts as first-class rows — never the
-- JSON snapshot as one opaque field. Timestamps are TEXT ISO-8601 UTC (RFC3339).
-- Booleans are INTEGER constrained to 0/1. Canonical uniqueness for rows with
-- optional columns uses STORED generated keys (NULLs would otherwise be distinct
-- under UNIQUE). FTS5 search tables are intentionally deferred to #54.

-- repository_state: structural singleton. Artifacts never change this Revision.
CREATE TABLE repository_state (
  id                         INTEGER PRIMARY KEY CHECK (id = 1),
  revision                   INTEGER NOT NULL,
  snapshot_id                TEXT NOT NULL,
  snapshot_schema            INTEGER NOT NULL,
  identity_algorithm_version TEXT NOT NULL,
  parser_metadata            TEXT NOT NULL,
  created_at                 TEXT NOT NULL,
  indexed_at                 TEXT NOT NULL
);

CREATE TABLE files (
  path        TEXT PRIMARY KEY CHECK (length(path) > 0),
  language    TEXT NOT NULL CHECK (length(language) > 0),
  content_hash TEXT NOT NULL CHECK (length(content_hash) > 0),
  size        INTEGER NOT NULL CHECK (size >= 0),
  modified_at TEXT NOT NULL,
  indexed_at  TEXT NOT NULL
);
CREATE INDEX idx_files_language ON files(language);

CREATE TABLE symbol_identities (
  symbol_id             TEXT PRIMARY KEY CHECK (length(symbol_id) > 0),
  language              TEXT NOT NULL CHECK (length(language) > 0),
  kind                  TEXT NOT NULL CHECK (length(kind) > 0),
  name                  TEXT NOT NULL,
  qualified_name        TEXT NOT NULL,
  parent_symbol_id      TEXT REFERENCES symbol_identities(symbol_id) ON DELETE SET NULL,
  signature_fingerprint TEXT NOT NULL,
  -- An occurrence-only symbol (no reliable logical identity) is modeled as a
  -- synthetic identity keyed by its occurrence handle, flagged here so the flat
  -- view and graph treat it as a single-occurrence node.
  occurrence_only       INTEGER NOT NULL DEFAULT 0 CHECK (occurrence_only IN (0, 1))
);
CREATE INDEX idx_identities_qualified ON symbol_identities(qualified_name);
CREATE INDEX idx_identities_parent ON symbol_identities(parent_symbol_id, kind, name);
CREATE INDEX idx_identities_lang_kind ON symbol_identities(language, kind);

CREATE TABLE symbol_occurrences (
  occurrence_id TEXT PRIMARY KEY CHECK (length(occurrence_id) > 0),
  symbol_id     TEXT NOT NULL REFERENCES symbol_identities(symbol_id) ON DELETE CASCADE,
  file_path     TEXT NOT NULL REFERENCES files(path) ON DELETE CASCADE,
  start_line    INTEGER NOT NULL CHECK (start_line >= 1),
  start_col     INTEGER NOT NULL CHECK (start_col >= 1),
  end_line      INTEGER NOT NULL CHECK (end_line >= start_line),
  end_col       INTEGER NOT NULL CHECK (end_col >= 1),
  signature     TEXT NOT NULL,
  code          TEXT NOT NULL,
  summary       TEXT NOT NULL,
  body_hash     TEXT NOT NULL,
  file_hash     TEXT NOT NULL,
  -- Within a single line the end column must not precede the start column.
  CHECK (end_line > start_line OR end_col >= start_col)
);
CREATE INDEX idx_occ_symbol ON symbol_occurrences(symbol_id);
CREATE INDEX idx_occ_file_range ON symbol_occurrences(file_path, start_line, start_col);

CREATE TABLE relations (
  relation_id       INTEGER PRIMARY KEY,
  kind              TEXT NOT NULL CHECK (length(kind) > 0),
  subject_symbol_id TEXT NOT NULL REFERENCES symbol_identities(symbol_id) ON DELETE CASCADE,
  object_symbol_id  TEXT REFERENCES symbol_identities(symbol_id) ON DELETE CASCADE,
  external_key      TEXT,
  direction         TEXT NOT NULL CHECK (length(direction) > 0),
  resolution        TEXT NOT NULL,
  ranking_confidence REAL NOT NULL CHECK (ranking_confidence >= 0.0 AND ranking_confidence <= 1.0),
  -- Canonical identity is stable even when object/external are NULL: a generated
  -- STORED key gives real uniqueness where a multi-column UNIQUE over NULLs cannot.
  canonical_key TEXT GENERATED ALWAYS AS (
    kind || char(31) || subject_symbol_id || char(31) ||
    coalesce(object_symbol_id, '') || char(31) ||
    coalesce(external_key, '') || char(31) || direction
  ) STORED,
  CHECK (object_symbol_id IS NOT NULL OR external_key IS NOT NULL OR resolution = 'unresolved')
);
CREATE UNIQUE INDEX idx_relations_canonical ON relations(canonical_key);
CREATE INDEX idx_relations_subject ON relations(subject_symbol_id, kind);
CREATE INDEX idx_relations_object ON relations(object_symbol_id, kind);

CREATE TABLE relation_evidence (
  evidence_id   TEXT PRIMARY KEY CHECK (length(evidence_id) > 0),
  relation_id   INTEGER NOT NULL REFERENCES relations(relation_id) ON DELETE CASCADE,
  occurrence_id TEXT REFERENCES symbol_occurrences(occurrence_id) ON DELETE SET NULL,
  file_path     TEXT,
  start_line    INTEGER,
  start_col     INTEGER,
  end_line      INTEGER,
  end_col       INTEGER,
  source        TEXT NOT NULL,
  provider      TEXT NOT NULL,
  tool_version  TEXT NOT NULL,
  method        TEXT NOT NULL,
  confidence    REAL NOT NULL CHECK (confidence >= 0.0 AND confidence <= 1.0),
  version_known INTEGER NOT NULL CHECK (version_known IN (0, 1)),
  detail        TEXT NOT NULL,
  detail_hash   TEXT NOT NULL
);
CREATE INDEX idx_evidence_relation ON relation_evidence(relation_id);

CREATE TABLE embedding_index_metadata (
  id               INTEGER PRIMARY KEY CHECK (id = 1),
  enabled          INTEGER NOT NULL CHECK (enabled IN (0, 1)),
  provider         TEXT NOT NULL,
  model            TEXT NOT NULL,
  dimension        INTEGER NOT NULL CHECK (dimension >= 0),
  template_version TEXT NOT NULL,
  distance         TEXT NOT NULL,
  -- Persisted vector encoding (e.g. float32-le-v1); an incompatible change forces
  -- a full embedding rebuild.
  encoding         TEXT NOT NULL DEFAULT '',
  built_at         TEXT NOT NULL
);

CREATE TABLE embeddings (
  symbol_id     TEXT PRIMARY KEY REFERENCES symbol_identities(symbol_id) ON DELETE CASCADE,
  occurrence_id TEXT REFERENCES symbol_occurrences(occurrence_id) ON DELETE SET NULL,
  content_hash  TEXT NOT NULL,
  dimension     INTEGER NOT NULL CHECK (dimension > 0),
  vector        BLOB NOT NULL
);

-- artifacts holds IMMUTABLE versions. There is no mutable status here (mutable
-- state lives in artifact_heads); a row is never updated after insert (except a
-- future GC). The canonical structured payload is validated upstream (#33); the
-- rendered Markdown is an optional cache from a trusted renderer.
CREATE TABLE artifacts (
  artifact_id           TEXT PRIMARY KEY CHECK (length(artifact_id) > 0),
  artifact_type         TEXT NOT NULL CHECK (length(artifact_type) > 0),
  logical_key           TEXT NOT NULL CHECK (length(logical_key) > 0),
  artifact_revision     INTEGER NOT NULL CHECK (artifact_revision >= 1),
  input_snapshot_id     TEXT NOT NULL,
  context_pack_hash     TEXT NOT NULL,
  policy_version        TEXT NOT NULL,
  prompt_version        TEXT NOT NULL,
  output_schema_version TEXT NOT NULL,
  renderer_version      TEXT NOT NULL,
  provider              TEXT NOT NULL,
  model                 TEXT NOT NULL,
  title                 TEXT NOT NULL,
  payload               TEXT NOT NULL,
  rendered_markdown     TEXT NOT NULL DEFAULT '',
  payload_hash          TEXT NOT NULL,
  duration_ms           INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
  metadata              TEXT NOT NULL,
  created_at            TEXT NOT NULL,
  UNIQUE (artifact_type, logical_key, artifact_revision)
);

CREATE TABLE artifact_dependencies (
  dependency_id   INTEGER PRIMARY KEY,
  artifact_id     TEXT NOT NULL REFERENCES artifacts(artifact_id) ON DELETE CASCADE,
  dependency_kind TEXT NOT NULL CHECK (dependency_kind IN ('symbol','occurrence','file','relation','snapshot','config')),
  symbol_id       TEXT,
  occurrence_id   TEXT,
  path            TEXT,
  relation_key    TEXT,
  content_hash    TEXT NOT NULL,
  -- Generated columns cannot be a PRIMARY KEY in SQLite, so canonical uniqueness
  -- over (artifact, kind, optional fields) is enforced by a UNIQUE index.
  canonical_key TEXT GENERATED ALWAYS AS (
    dependency_kind || char(31) || coalesce(symbol_id, '') || char(31) ||
    coalesce(occurrence_id, '') || char(31) || coalesce(path, '') || char(31) ||
    coalesce(relation_key, '')
  ) STORED
);
CREATE UNIQUE INDEX idx_artifact_deps_canonical ON artifact_dependencies(artifact_id, canonical_key);
CREATE INDEX idx_artifact_deps_symbol ON artifact_dependencies(symbol_id);
CREATE INDEX idx_artifact_deps_path ON artifact_dependencies(path);

-- artifact_heads is the MUTABLE state per (type, logical key): which immutable
-- version is current, the latest revision, and freshness. The head keeps pointing
-- at the last valid version even when status is stale or a later refresh failed.
CREATE TABLE artifact_heads (
  artifact_type      TEXT NOT NULL,
  logical_key        TEXT NOT NULL,
  current_artifact_id TEXT REFERENCES artifacts(artifact_id) ON DELETE SET NULL,
  latest_revision    INTEGER NOT NULL DEFAULT 0 CHECK (latest_revision >= 0),
  status             TEXT NOT NULL CHECK (status IN ('not_generated','current','stale','failed')),
  stale_reason       TEXT NOT NULL DEFAULT '',
  last_error_code    TEXT NOT NULL DEFAULT '',
  last_error_message TEXT NOT NULL DEFAULT '',
  updated_at         TEXT NOT NULL,
  PRIMARY KEY (artifact_type, logical_key)
);
