# Frontmatter YAML Schema (v0.1)

Generated Markdown notes written to the vault by `symingest` include a YAML frontmatter block that defines metadata about the document. This schema is structured to be compatible with Obsidian, `symseek`, and the broader Symaira ecosystem.

## Frontmatter Fields

| Field | Go Type | YAML Representation | Description |
|---|---|---|---|
| `source_path` | `string` | `source_path: <path>` | The absolute path to the original ingested file on the local filesystem. |
| `ingested_at` | `time.Time` | `ingested_at: <ISO-8601>` | UTC timestamp when the document was successfully ingested. |
| `sha256` | `string` | `sha256: <hash>` | SHA-256 hash of the original file content, used for duplicate detection. |
| `mime` | `string` | `mime: <mime-type>` | Normalized MIME type detected for the source file (e.g. `application/pdf`, `image/png`, `text/plain`). |
| `tags` | `[]string` | `tags: [...]` | Obsidian-compatible tag list. Initialized to `[]` by default. |
| `category` | `string` | `category: ""` | Document classification category. Initialized to `""` by default. |
| `correspondent` | `string` | `correspondent: <name>` | Resolved correspondent/sender name. Omitted if not classified. |
| `document_type` | `string` | `document_type: <name>` | Resolved document type/category label. Omitted if not classified. |
| `ocr_engine` | `string` | `ocr_engine: <engine>` | Name of the OCR/extraction engine used (e.g. `tesseract`, `pdftoppm+tesseract`). Omitted if no OCR was performed. |
| `archive_path` | `string` | `archive_path: ""` | Path to the safely archived copy of the original source file. Initialized to `""` by default. |
| `paperless` | `*PaperlessMeta` | `paperless: {...}` | Traceability metadata from a migrated Paperless-ngx document. Omitted entirely for non-migrated sources. See "Paperless metadata block" below. |

## Paperless metadata block

When a note originates from a Paperless-ngx migration (`symingest import paperless`), the frontmatter carries a nested `paperless` block so the note can be traced back to the original record and migration completeness can be audited.

| Field | Go Type | YAML Representation | Description |
|---|---|---|---|
| `document_id` | `int` | `document_id: <id>` | The original Paperless document ID. |
| `title` | `string` | `title: <title>` | The document title as stored in Paperless. Omitted if empty. |
| `created` | `time.Time` | `created: <ISO-8601>` | The document's creation timestamp in Paperless. Omitted if not available. |
| `added` | `time.Time` | `added: <ISO-8601>` | When the document was added to Paperless. Omitted if not available. |
| `modified` | `time.Time` | `modified: <ISO-8601>` | When the document was last modified in Paperless. Omitted if not available. |
| `storage_path` | `string` | `storage_path: <name>` | Resolved Paperless storage path name. Omitted if not set. |
| `original_file_name` | `string` | `original_file_name: <name>` | The original uploaded file name in Paperless. Omitted if empty. |
| `archived_file_name` | `string` | `archived_file_name: <name>` | The archived (OCR'd) file name in Paperless. Omitted if empty. |
| `page_count` | `int` | `page_count: <n>` | Page count reported by Paperless. Omitted if zero. |
| `url` | `string` | `url: <url>` | Backlink to the original document in the Paperless web UI. Omitted if empty. |

### Storage path layout

By default, migrated notes are written flat into the vault root. When the `--preserve-storage-paths` option is enabled on `symingest import paperless`, the generated Markdown file is placed under a subdirectory derived from `storage_path` instead. For example, a document whose `storage_path` is `Finance/Invoices` becomes `vault/Finance/Invoices/<name>.md` rather than `vault/<name>.md`.

Mapping rules:

- Path separators (`/` or `\`) are treated as nested directory boundaries.
- Each segment is stripped of leading/trailing spaces and dots, and of characters unsafe on common filesystems (`< > : " | ? * / \`). Those characters are replaced with `_`.
- Empty segments and the traversal segments `.` and `..` are dropped, so the resulting path can never escape the vault.
- If multiple documents resolve to the same directory and base name, a deterministic numeric suffix (`-2`, `-3`, …) is appended until a unique name is found.

The original `storage_path` value is always preserved in the `paperless` block regardless of layout mode, so the mapping can be reconstructed or audited later.

## Example Frontmatter Blocks

### Ingested PDF with OCR
```yaml
---
source_path: /Users/daniel/Scans/invoice.pdf
ingested_at: 2026-06-26T15:00:00Z
sha256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
mime: application/pdf
tags: []
category: ""
ocr_engine: pdftoppm+tesseract
archive_path: ""
---
```

### Ingested Plain Text (No OCR)
```yaml
---
source_path: /Users/daniel/Notes/todo.txt
ingested_at: 2026-06-26T15:00:00Z
sha256: 09c2a6136fa512ba5c24e754d92427ae41e4649b934ca495991b7852b85514b8a1
mime: text/plain
tags: []
category: ""
archive_path: ""
---
```

### Migrated from Paperless-ngx
```yaml
---
source_path: /tmp/symingest-import-12345.pdf
ingested_at: 2026-06-30T20:00:00Z
sha256: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
mime: application/pdf
tags:
    - financial
category: Invoice
correspondent: Acme Corp
document_type: Invoice
ocr_engine: pdftoppm+tesseract
archive_path: ""
paperless:
    document_id: 42
    title: Migrated Invoice
    created: 2024-03-01T09:00:00Z
    added: 2024-03-02T10:00:00Z
    modified: 2024-03-05T11:30:00Z
    storage_path: Invoices/2024
    original_file_name: invoice-original.pdf
    archived_file_name: invoice-archived.pdf
    page_count: 3
    url: https://paperless.local/documents/42
---
```
