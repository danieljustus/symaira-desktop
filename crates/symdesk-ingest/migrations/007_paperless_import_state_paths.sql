ALTER TABLE paperless_import_state ADD COLUMN vault_path TEXT;
ALTER TABLE paperless_import_state ADD COLUMN archive_path TEXT;
ALTER TABLE paperless_import_state ADD COLUMN sha256 TEXT;

CREATE INDEX IF NOT EXISTS idx_paperless_import_state_hash
    ON paperless_import_state(base_url, sha256);
