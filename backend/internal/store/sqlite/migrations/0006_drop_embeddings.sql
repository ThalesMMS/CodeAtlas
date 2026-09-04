-- Schema v6 (issue #163) drops dense retrieval. The vectors served exactly one
-- feature, the global /api/search endpoint, which is removed with them; every
-- other surface (hover, see more, codemaps, deepwiki) reaches the index through
-- FTS5/BM25 and never read these tables.
--
-- Forward-only: a database that has applied this migration cannot be opened by a
-- build that predates it, because migrate() rejects a database newer than the
-- embedded migration set.

DROP TABLE IF EXISTS embeddings;
DROP TABLE IF EXISTS embedding_index_metadata;
