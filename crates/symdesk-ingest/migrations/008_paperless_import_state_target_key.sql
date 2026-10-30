CREATE TABLE IF NOT EXISTS paperless_import_state_v2 (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    base_url TEXT NOT NULL,
    target_vault TEXT NOT NULL DEFAULT '',
    target_archive TEXT NOT NULL DEFAULT '',
    paperless_document_id INTEGER NOT NULL,
    status TEXT NOT NULL,
    last_error TEXT,
    vault_path TEXT,
    archive_path TEXT,
    sha256 TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(base_url, target_vault, target_archive, paperless_document_id)
);

INSERT OR REPLACE INTO paperless_import_state_v2 (
    id,
    base_url,
    target_vault,
    target_archive,
    paperless_document_id,
    status,
    last_error,
    vault_path,
    archive_path,
    sha256,
    created_at,
    updated_at
)
SELECT
    id,
    base_url,
    COALESCE(target_vault, ''),
    COALESCE(target_archive, ''),
    paperless_document_id,
    status,
    last_error,
    vault_path,
    archive_path,
    sha256,
    created_at,
    updated_at
FROM paperless_import_state;

DROP TABLE paperless_import_state;
ALTER TABLE paperless_import_state_v2 RENAME TO paperless_import_state;

CREATE INDEX IF NOT EXISTS idx_paperless_import_state_status
    ON paperless_import_state(base_url, status);
CREATE INDEX IF NOT EXISTS idx_paperless_import_state_target_status
    ON paperless_import_state(base_url, target_vault, target_archive, status);
CREATE INDEX IF NOT EXISTS idx_paperless_import_state_hash
    ON paperless_import_state(base_url, sha256);
CREATE INDEX IF NOT EXISTS idx_paperless_import_state_document
    ON paperless_import_state(base_url, paperless_document_id);
