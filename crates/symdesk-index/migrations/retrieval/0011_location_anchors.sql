-- Preserve durable source locations for search hits. Existing rows remain valid
-- with an empty anchor and are populated on the next re-index.
ALTER TABLE chunks ADD COLUMN anchor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE chunks ADD COLUMN anchor_value TEXT NOT NULL DEFAULT '';
