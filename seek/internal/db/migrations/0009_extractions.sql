-- Persist grounded extraction sidecars (produced externally, e.g. by
-- symingest's annotate package) as first-class rows linked to the document
-- and, where determinable, the best matching chunk.
CREATE TABLE IF NOT EXISTS extractions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    document_path TEXT NOT NULL,
    chunk_id INTEGER,
    class TEXT NOT NULL,
    value TEXT NOT NULL,
    evidence_text TEXT NOT NULL DEFAULT '',
    span_start INTEGER,
    span_end INTEGER,
    matched INTEGER NOT NULL DEFAULT 0,
    producer TEXT NOT NULL DEFAULT '',
    source_ref TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    FOREIGN KEY(document_path) REFERENCES documents(path) ON DELETE CASCADE,
    FOREIGN KEY(chunk_id) REFERENCES chunks(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_extractions_doc_path ON extractions(document_path);
CREATE INDEX IF NOT EXISTS idx_extractions_class ON extractions(class);

CREATE VIRTUAL TABLE IF NOT EXISTS extractions_fts USING fts5(
    value,
    evidence_text,
    content='extractions',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS extractions_ai AFTER INSERT ON extractions BEGIN
    INSERT INTO extractions_fts(rowid, value, evidence_text) VALUES (new.id, new.value, new.evidence_text);
END;

CREATE TRIGGER IF NOT EXISTS extractions_ad AFTER DELETE ON extractions BEGIN
    INSERT INTO extractions_fts(extractions_fts, rowid, value, evidence_text) VALUES('delete', old.id, old.value, old.evidence_text);
END;

CREATE TRIGGER IF NOT EXISTS extractions_au AFTER UPDATE ON extractions BEGIN
    INSERT INTO extractions_fts(extractions_fts, rowid, value, evidence_text) VALUES('delete', old.id, old.value, old.evidence_text);
    INSERT INTO extractions_fts(rowid, value, evidence_text) VALUES (new.id, new.value, new.evidence_text);
END;
