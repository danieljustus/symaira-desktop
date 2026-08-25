ALTER TABLE paperless_import_state ADD COLUMN target_vault TEXT NOT NULL DEFAULT '';
ALTER TABLE paperless_import_state ADD COLUMN target_archive TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_paperless_import_state_target_status
    ON paperless_import_state(base_url, target_vault, target_archive, status);
