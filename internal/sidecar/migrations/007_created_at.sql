-- Preserve the vault frontmatter creation timestamp separately from the
-- on-disk modification timestamp used by modified: search filters.
ALTER TABLE files ADD COLUMN created_at TEXT;
CREATE INDEX IF NOT EXISTS idx_files_created_at ON files(created_at);
