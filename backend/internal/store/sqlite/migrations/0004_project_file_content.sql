-- Persist a bounded, scanner-owned preview for allowlisted non-code project
-- files. Code files keep the default empty content because their symbol
-- occurrences already carry the relevant source slices.
ALTER TABLE files
  ADD COLUMN content TEXT NOT NULL DEFAULT '';

ALTER TABLE files
  ADD COLUMN content_truncated INTEGER NOT NULL DEFAULT 0
  CHECK (content_truncated IN (0, 1));
