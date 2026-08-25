CREATE TABLE IF NOT EXISTS classification_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pattern TEXT NOT NULL,
    kind TEXT NOT NULL, -- 'category', 'tag', 'correspondent', 'document_type'
    value TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_classification_rules_kind ON classification_rules(kind);

ALTER TABLE documents ADD COLUMN category TEXT;
ALTER TABLE documents ADD COLUMN tags TEXT; -- JSON array of tags
ALTER TABLE documents ADD COLUMN correspondent TEXT;
ALTER TABLE documents ADD COLUMN document_type TEXT;
