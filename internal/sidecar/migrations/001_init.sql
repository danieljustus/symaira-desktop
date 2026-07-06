CREATE TABLE files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    path TEXT UNIQUE NOT NULL,
    sha256 TEXT NOT NULL,
    title TEXT NOT NULL,
    modified_at DATETIME NOT NULL,
    indexed_at DATETIME NOT NULL
);

CREATE TABLE file_properties (
    file_id INTEGER NOT NULL,
    key TEXT NOT NULL,
    value TEXT,
    value_type TEXT NOT NULL,
    PRIMARY KEY (file_id, key),
    FOREIGN KEY (file_id) REFERENCES files(id) ON DELETE CASCADE
);

CREATE TABLE links (
    from_path TEXT NOT NULL,
    to_path TEXT NOT NULL,
    kind TEXT NOT NULL,
    PRIMARY KEY (from_path, to_path, kind)
);

CREATE TABLE view_presets (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    filter_json TEXT NOT NULL,
    sort_json TEXT NOT NULL,
    columns_json TEXT NOT NULL
);

-- FTS5 table for Leitplanke 3
CREATE VIRTUAL TABLE fts_search USING fts5(
    title,
    body,
    content='files',
    content_rowid='id'
);


