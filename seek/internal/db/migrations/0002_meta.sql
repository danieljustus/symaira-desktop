CREATE TABLE IF NOT EXISTS index_meta (
    key TEXT PRIMARY KEY,
    value INTEGER NOT NULL DEFAULT 0
);

INSERT OR IGNORE INTO index_meta (key, value) VALUES ('generation', 0);
