-- Contract v2: first-class document metadata columns on files table.
-- Additive migration: existing rows get NULL defaults, no data loss.

ALTER TABLE files ADD COLUMN document_date TEXT;      -- ISO-8601 date the doc refers to
ALTER TABLE files ADD COLUMN person TEXT;              -- household member
ALTER TABLE files ADD COLUMN status TEXT;              -- enum: open|paid|submitted|done|needs_review|waiting_for_reply
ALTER TABLE files ADD COLUMN due_date TEXT;            -- ISO-8601 date deadline
ALTER TABLE files ADD COLUMN confidence INTEGER;       -- 0-100 classification confidence
ALTER TABLE files ADD COLUMN ocr_json_path TEXT;       -- path to plain-text OCR JSON
ALTER TABLE files ADD COLUMN simhash TEXT;             -- 64-bit SimHash hex

-- Indexes for filter queries
CREATE INDEX IF NOT EXISTS idx_files_status ON files(status);
CREATE INDEX IF NOT EXISTS idx_files_person ON files(person);
CREATE INDEX IF NOT EXISTS idx_files_document_date ON files(document_date);
CREATE INDEX IF NOT EXISTS idx_files_due_date ON files(due_date);
CREATE INDEX IF NOT EXISTS idx_files_confidence ON files(confidence);
