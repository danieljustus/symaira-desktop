-- Derived per-document retrieval lifecycle diagnostics. Markdown remains the
-- source of truth; this table can be rebuilt by an index pass.
CREATE TABLE IF NOT EXISTS index_lifecycle (
    path TEXT PRIMARY KEY,
    state TEXT NOT NULL CHECK (state IN ('queued', 'indexing', 'indexed', 'failed', 'encrypted', 'unsupported')),
    reason TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_index_lifecycle_state ON index_lifecycle(state);
