-- Add source character span columns so chunks can be traced back to their
-- exact byte offsets in the original document text. Nullable so existing
-- rows (indexed before this migration) remain valid; they are backfilled
-- lazily on the next re-index rather than in this migration.
ALTER TABLE chunks ADD COLUMN char_start INTEGER;
ALTER TABLE chunks ADD COLUMN char_end INTEGER;
