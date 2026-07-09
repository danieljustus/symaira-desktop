# Symaira Vault Contract

**contract_version: 2**

This document specifies the format and constraints of a Symaira Vault. Any application or service interacting with the Vault MUST comply with this contract to ensure interoperability across the ecosystem (e.g., `symaira-desktop`, `symaira-ingest`, `symaira-seek`).

> **Backwards compatibility:** Contract v2 is additive. Parsers MUST preserve unknown fields. Vaults using only v1 fields continue to work; the new optional fields are only meaningful for document-oriented notes.

## 1. Vault Structure
- **Root Directory:** A Vault is a directory on the local filesystem containing Markdown files and optional attachments.
- **Ignore Rules:** Any directory starting with a dot (e.g., `.obsidian`, `.trash`, `.git`) MUST be ignored by all parsers and indexers.
- **Nested Folders:** Files can be stored flat in the root or nested in subdirectories. Folder names MUST be sanitized (no special characters or path traversal).

## 2. File Format
- **Extension:** All notes MUST have the `.md` extension and contain valid Markdown text.
- **Frontmatter:** All notes MUST have a YAML frontmatter block at the very beginning of the file, enclosed by `---`.

## 3. Frontmatter Schema
The following YAML fields are defined by this contract. Parsers MUST preserve unknown fields but MUST ensure the presence of required fields.

### Required Fields
- `title` (string): A human-readable title for the note.
- `created` (string, ISO-8601): The creation timestamp of the note.
- `tags` (array of strings): A list of tags. Can be empty `[]`.

### Optional/Integration Fields (e.g., for `symaira-ingest`)
The contract fully accepts and standardizes the following fields commonly written by `symingest`:
- `source_path` (string): The path of the source document from which this note was created.
- `imported_from` (string): The system the document was imported from (e.g., "paperless").
- `import_run_id` (string): A unique identifier for the import batch.
- `source_uri` (string): The URI of the source document.
- `download_uri` (string): The URI where the document can be downloaded.
- `ingested_at` (string, ISO-8601): The timestamp when the document was ingested.
- `sha256` (string): The SHA256 hash of the original document.
- `mime` (string): The MIME type of the original document.
- `category` (string): A document category.
- `correspondent` (string): The correspondent or author of the original document. When this value matches an existing note title in the vault, a `correspondent` link edge is recorded so backlinks answer "all documents from X".
- `document_type` (string): The type of the document (e.g., "invoice").
- `ocr_engine` (string): The OCR engine used to extract text.
- `archive_path` (string): A link to the archived original file.
- `paperless` (object): Traceability metadata from Paperless-ngx.

### Document Metadata Fields (contract_version 2)
The following optional fields provide first-class document query metadata. They are additive and backwards-compatible; parsers MUST ignore unknown fields and v1 vaults continue to index normally.
- `document_date` (string, ISO-8601 date, e.g. `2026-07-01`): The date the document *refers to*, distinct from `created` (when the note was written). Used for date-range queries (e.g., "all invoices from July 2026").
- `person` (string): The household member the document belongs to. Enables per-person filtering (e.g., "all documents for Alice").
- `status` (enum string): Workflow status of the document. Allowed values:
  - `open` — newly created, not yet processed
  - `paid` — payment completed
  - `submitted` — sent to a third party
  - `done` — fully processed and closed
  - `needs_review` — requires human attention
  - `waiting_for_reply` — awaiting a response from the correspondent
- `due_date` (string, ISO-8601 date, e.g. `2026-08-01`): The deadline or due date for the document (e.g., payment due).
- `confidence` (integer, 0–100): Classification confidence from the ingest pipeline. 100 means certain, 0 means unknown.
- `ocr_json_path` (string): Filesystem path to a plain-text OCR JSON file stored next to the original document.
- `simhash` (string, 16-char hex): 64-bit text SimHash for near-duplicate / template detection.

## 4. Wikilink Semantics
- **Syntax:** `[[Filename]]` or `[[Filename|Display Text]]`.
- **Target:** Wikilinks link to other Markdown files in the vault by their base name (without `.md`).
- **Resolution:** Resolution is case-insensitive. If multiple files have the same name in different folders, the behavior is undefined (it is recommended to use unique names or fully qualified paths).

## 5. Attachments
- Attachments (images, PDFs) should be referenced using standard Markdown links `[Title](path/to/file.pdf)` or embedded via `![Title](path/to/image.png)`.
- Attachments can be stored anywhere in the vault, though an `assets/` or `attachments/` subfolder is recommended.

## 6. Templates (contract_version 2)
- Reusable note templates SHOULD be stored in the `templates/` folder at the root of the vault.
- Templates are valid Markdown files (`.md`) containing optional placeholders that are substituted on note creation.
- Standard placeholders:
  - `{{title}}` — Substituted with the title of the note.
  - `{{date}}` — Substituted with the current date (YYYY-MM-DD).
  - `{{time}}` — Substituted with the current time (HH:MM).
- When a template is applied, its content (including any frontmatter defined in the template) is merged into the contract-conform base frontmatter.

