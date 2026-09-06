-- Contract v3: document kind classification (note|document|meeting).
-- Additive migration: existing rows get 'note' default.
ALTER TABLE files ADD COLUMN "type" TEXT NOT NULL DEFAULT 'note';

-- Index for sidebar filtering (notes vs documents vs meetings)
CREATE INDEX IF NOT EXISTS idx_files_type ON files("type");
