-- Cache the on-disk file size and modification time alongside the existing
-- content hash so a startup index refresh can skip re-reading and
-- re-hashing a file whose stat still matches what was recorded at the last
-- index. NULL means no cached stat is available (rows written before this
-- migration, or indexed from already-read bytes rather than a path); the
-- refresh fast path treats NULL as "cannot skip" and falls back to the
-- existing SHA-256 based check.
ALTER TABLE files ADD COLUMN size INTEGER;
ALTER TABLE files ADD COLUMN mtime_ns INTEGER;
