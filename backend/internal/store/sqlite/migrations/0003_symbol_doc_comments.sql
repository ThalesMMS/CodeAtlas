-- Doc comments are occurrence evidence: duplicate declarations may share one
-- logical SymbolID while carrying different documentation at each source range.
ALTER TABLE symbol_occurrences
  ADD COLUMN doc_comment TEXT NOT NULL DEFAULT '';
