-- Dataset rows are derived from raw files under datasets/<slug> in the Markdown vault.
-- The composite key makes repeated and overlapping imports idempotent.
CREATE TABLE IF NOT EXISTS dataset_rows (
    dataset_slug TEXT NOT NULL,
    row_key TEXT NOT NULL,
    identity TEXT,
    values_json TEXT NOT NULL,
    source_path TEXT NOT NULL,
    row_number INTEGER NOT NULL,
    PRIMARY KEY (dataset_slug, row_key)
);

CREATE INDEX IF NOT EXISTS idx_dataset_rows_dataset ON dataset_rows(dataset_slug);
