-- FTS5 lexical index (issue #54). One row per SymbolID built from a canonical
-- (primary) occurrence; identifier expansion (camelCase/acronym/digit) is done in
-- Go by internal/lexical before storage, so unicode61 only splits the already
-- expanded, lower-cased tokens. Field weight is expressed by separate columns and
-- bm25() weights in the search policy — not by repeating text. This is an ordinary
-- (own-content) FTS5 table maintained EXPLICITLY in the ChangeSet write transaction
-- (no implicit triggers); it is a derived index, reconstructible from identities/
-- occurrences, and does not participate in the SnapshotID. Requires the `fts5`
-- build tag (mattn/go-sqlite3).

CREATE VIRTUAL TABLE fts_symbols USING fts5(
  symbol_id UNINDEXED,
  name_tokens,
  qualified_name_tokens,
  kind_tokens,
  path_tokens,
  signature_tokens,
  summary_tokens,
  code_tokens,
  tokenize = 'unicode61'
);

-- Records the tokenizer contract version the FTS content was built with; an
-- incompatible tokenizer change requires a full FTS rebuild.
CREATE TABLE fts_metadata (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  tokenizer_version TEXT NOT NULL,
  built_at          TEXT NOT NULL
);
