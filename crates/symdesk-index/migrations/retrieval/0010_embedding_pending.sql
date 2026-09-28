-- Record whether a chunk's embedding was actually produced by the configured
-- backend or is a pending placeholder (the local-hash fallback was used because
-- the backend was unavailable). Pending chunks carry no semantic vector and are
-- excluded from the vector search leg until re-embedded.
--
-- Default 0 keeps existing rows (indexed with a real backend) valid; new rows
-- set this to 1 only for unembeddable chunks.
ALTER TABLE chunks ADD COLUMN embedding_pending INTEGER NOT NULL DEFAULT 0;
