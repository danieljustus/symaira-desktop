CREATE TABLE IF NOT EXISTS paperless_import_state (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    base_url TEXT NOT NULL,
    paperless_document_id INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending', -- 'pending', 'imported', 'skipped', 'failed'
    last_error TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(base_url, paperless_document_id)
);

CREATE INDEX IF NOT EXISTS idx_paperless_import_state_status ON paperless_import_state(base_url, status);
