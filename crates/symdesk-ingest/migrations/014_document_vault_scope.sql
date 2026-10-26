-- sqlitekit executes each migration in one transaction. Defer the existing
-- jobs/extractions foreign keys until documents_v2 has replaced documents.
PRAGMA defer_foreign_keys = ON;

CREATE TABLE documents_v2 (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_path TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    mime TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    vault_path TEXT,
    archive_path TEXT,
    category TEXT,
    tags TEXT,
    correspondent TEXT,
    document_type TEXT,
    source_mail_id TEXT,
    vault_root TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(sha256, vault_root)
);

INSERT INTO documents_v2 (
    id, source_path, sha256, mime, status, vault_path, archive_path,
    category, tags, correspondent, document_type, source_mail_id,
    vault_root, created_at, updated_at
)
SELECT
    id, source_path, sha256, mime, status, vault_path, archive_path,
    category, tags, correspondent, document_type, source_mail_id,
    '', created_at, updated_at
FROM documents;

CREATE TABLE jobs_v2 (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(document_id) REFERENCES documents_v2(id) ON DELETE CASCADE
);

INSERT INTO jobs_v2 (id, document_id, kind, status, attempts, last_error, created_at, updated_at)
SELECT id, document_id, kind, status, attempts, last_error, created_at, updated_at
FROM jobs;

CREATE TABLE document_extractions_v2 (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id INTEGER NOT NULL,
    profile TEXT NOT NULL,
    field_type TEXT NOT NULL,
    value TEXT NOT NULL,
    start_offset INTEGER NOT NULL DEFAULT 0,
    end_offset INTEGER NOT NULL DEFAULT 0,
    snippet TEXT NOT NULL DEFAULT '',
    extracted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (document_id) REFERENCES documents_v2(id)
);

INSERT INTO document_extractions_v2 (
    id, document_id, profile, field_type, value, start_offset, end_offset,
    snippet, extracted_at, created_at
)
SELECT
    id, document_id, profile, field_type, value, start_offset, end_offset,
    snippet, extracted_at, created_at
FROM document_extractions;

DROP TABLE document_extractions;
DROP TABLE jobs;
DROP TABLE documents;
ALTER TABLE documents_v2 RENAME TO documents;
ALTER TABLE jobs_v2 RENAME TO jobs;
ALTER TABLE document_extractions_v2 RENAME TO document_extractions;

CREATE INDEX IF NOT EXISTS idx_documents_sha256 ON documents(sha256);
CREATE INDEX IF NOT EXISTS idx_documents_vault_root ON documents(vault_root);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_document_extractions_doc_profile
    ON document_extractions(document_id, profile, field_type);
