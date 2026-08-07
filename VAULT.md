# Symaira Vault Contract

**contract_version: 3**

This document specifies the format and constraints of a Symaira Vault. Any application or service interacting with the Vault MUST comply with this contract to ensure interoperability across the ecosystem (e.g., `symaira-desktop`, `symaira-ingest`, `symaira-seek`).

> **Backwards compatibility:** Contract v3 is additive. Parsers MUST preserve unknown fields. Vaults using only v1/v2 fields continue to work; the new optional fields are only meaningful for document-oriented notes.

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

### Document Kind (contract_version 3)
The `type` field classifies every markdown file in the vault into one of three kinds:

- `note` (default): A free-form note or journal entry.
- `document`: An imported or ingested document with structured metadata (e.g. invoices, letters, contracts).
- `meeting`: A meeting note imported by `symmeet` (see section 8).

**Explicit declaration:** A file with `type: note`, `type: document`, or `type: meeting` in its frontmatter is classified accordingly. Meeting-specific fields (section 8) SHOULD be paired with `type: meeting`.

**Inference when absent:** A file with no `type` field is resolved at index time by the following rules (evaluated in order; the first match wins):
1. If the frontmatter contains any of `source_path`, `mime`, `sha256`, `document_date`, or `asn` → the file is classified as `document`.
2. If the frontmatter contains `meeting_id` → the file is classified as `meeting`.
3. Otherwise → the file is classified as `note`.

> **Backwards compatibility:** Contract v3 adds the `type` field. Existing vaults work without it — every file without `type` is classified at index time by inference. Parsers MUST treat an absent `type` as `note` when no inference triggers.

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
- `asn` (positive integer): A vault-wide unique archive serial number for the physical paper archive. It is optional, but when present it MUST be a YAML integer greater than zero and MUST not be assigned to any other note in the vault. `symdesk doc asn <file> next` allocates the lowest available number; `symdesk doctor` reports malformed or duplicate assignments.

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

## 7. Version History & Trash (contract_version 2)
- symdesk keeps a local safety net inside the hidden `.symdesk/` folder at the vault root; it is never indexed and never synced by symdesk itself.
  - `.symdesk/history/objects/` — content-addressed snapshot blobs (SHA-256).
  - `.symdesk/history/manifest/` — per-file JSON manifests of snapshot metadata.
  - `.symdesk/trash/` — soft-deleted files plus `*.trashinfo.json` metadata (original path, deletion time).
- **Snapshots:** every mutating symdesk operation (note creation/overwrite, property edits, `doc status|due|asn`, clipping — including all MCP/AI writes) records the file's prior content first. Identical content is deduplicated.
- **CLI:**
  - `symdesk history <file>` — list snapshots; `symdesk history prune` — apply retention.
  - `symdesk restore <file> [--at <id>]` — restore a snapshot (latest by default). The pre-restore state is snapshotted, so restores are undoable. The sidecar is re-indexed automatically.
  - `symdesk note delete <file>` — move a note to the trash (removed from the index).
  - `symdesk trash list | restore <name> | purge [--older-than-days N | --all]`.
- **Retention (configurable via config file or environment):**
  - `history_max_per_file` / `SYMDESK_HISTORY_MAX_PER_FILE` — max snapshots kept per file on prune (default 20, 0 = unlimited).
  - `history_max_age_days` / `SYMDESK_HISTORY_MAX_AGE_DAYS` — snapshots older than this are pruned; the newest snapshot per file is always kept (default 90, 0 = unlimited).
  - `history_checkpoint_max_age_days` / `SYMDESK_HISTORY_CHECKPOINT_MAX_AGE_DAYS` — task checkpoints older than this are pruned; their blobs are then no longer protected from garbage collection (default 30, 0 = unlimited).
  - `trash_retention_days` / `SYMDESK_TRASH_RETENTION_DAYS` — default age threshold for `trash purge` (default 30).

## 8. Meeting Notes (contract_version 2)
`symdesk meeting import` creates a reviewed meeting note from a `symmeet` artifact. Integration is runtime-only (PATH detection, no compile-time or bundling dependency on `symmeet`) and entirely optional: a vault with no meeting notes, or opened where `symmeet` is absent, behaves exactly as a v1/v2 vault always has.

- `type` (string, `"meeting"`): marks a note as an imported meeting. Notes without this field are unaffected by any meeting-specific behavior.
- `meeting_id` (string): the source `symmeet` meeting UUID. Re-importing the same ID updates the same note (`meetings/meeting-<id>.md`).
- `started_at` / `ended_at` (string, ISO-8601): the meeting's recorded time range.
- `duration_ms` (integer): `ended_at - started_at` in milliseconds.
- `language` (string): the transcript language, when known.
- `participants` (array of objects): one entry per known speaker.
  - `label` (string): the speaker's display label (falls back to the raw anonymous `speaker_id` when unlabeled).
  - `speaker_ids` (array of strings): the meeting-local anonymous speaker ID(s) this participant covers.
  - `entity_id` (string, optional): set only by an explicit, separately reviewed participant-confirmation step. SymDesk never writes this automatically.
  - `contact_ref` (object, optional): an opaque reference to the authoritative `symrelate` contact, set only by an explicit, reviewed link step (`symdesk meeting participant link-contact`). It follows symrelate's published reference-only contract: `provider` (`"symrelate"`), `schema_version` (integer), `id` (stable opaque contact ID), `kind` (`"person"`/`"organization"`), and `display_name` as a refreshable rendering cache — identity is `id` + `kind`. It never contains contact points (email/phone/address/URL/handle), notes, transcript text, or local paths, and unknown additive fields are preserved on round-trip. Linking never creates a contact in `symrelate`; unlinking (`unlink-contact`) removes only the reference. The integration is runtime-only and optional: with `symrelate` absent, a contact erased, or a `schema_version` SymDesk does not understand, the note and its stored reference simply keep rendering as-is.
- `symmeet_source` (object): artifact provenance.
  - `artifact_schema_version` (integer): the meeting artifact schema version the note was imported from.
  - `review_state` (string): `"unreviewed"` on import; set to `"reviewed"` by the user's own workflow (SymDesk does not change it automatically).

The note body wraps the transcript in a pair of markers:
```text
<!-- symmeet-transcript:start -->
...transcript markdown from `symmeet export --format markdown`...
<!-- symmeet-transcript:end -->
```
`symdesk meeting refresh` only ever replaces the content strictly between these markers with a freshly exported transcript (which itself prefers `symmeet`-side edited segments over raw engine output). Anything written outside the markers — meeting notes, follow-ups, links — and the note's frontmatter (including any reviewed `entity_id`/`review_state`) survive a refresh untouched. If the markers are missing (e.g. the block was manually restructured), refresh fails safely instead of guessing where the transcript is. Refresh previews its diff by default; a `--apply` flag is required to write it.

SymDesk never mutates the raw `symmeet` artifact directory: corrections to speaker labels or transcript text happen through `symmeet` itself, and the next import or refresh picks them up.
- Trashing a file snapshots its final content first, so even a purged trash item stays recoverable until history retention drops it.

## 9. Meeting Knowledge Publishing (contract_version 2)
`symdesk meeting publish` sends a reviewer-approved set of facts and participant relations from a meeting note (section 8) to Symaira Memory (`symmemory`). Nothing about a meeting is written to Memory automatically — publish requires the explicit command, reviewer-authored facts, and participants whose `entity_id` was already confirmed (see section 8: "SymDesk never writes this automatically").

- `symmeet_published_facts` (array of strings, optional): SHA-256 hex hashes of the content of every fact/decision/action item already published to Memory for this note. Written automatically by `symdesk meeting publish` after each successful write; never written manually. `symmemory set` is not idempotent (two calls with identical content create two separate memories), so this ledger is what makes re-applying the same reviewed proposal safe — already-published facts are skipped instead of resubmitted.
- **Meeting entity:** a Memory entity named literally `Meeting <meeting_id>` (type `other`) represents the meeting itself. It is created on first publish, or reused if it already exists.
- **Attended relation:** for every participant with a confirmed `entity_id` (section 8), publish creates an `attended` relation from that person's Memory entity to the meeting entity. Relations are idempotent at the symmemory layer, so creating them on every publish is safe and expected.
- Every published fact is stored in Memory scoped to (linked to) the meeting entity, so every write traces back to its source meeting. Reviewers may optionally prepend/append a transcript segment timestamp to a fact's text before publishing, as an additional evidence marker; SymDesk does not enforce or structure this — it is free text the reviewer controls.
- Content published to Memory passes through symmemory's own automatic PII redaction (email addresses, phone numbers, API keys/credentials, credit card numbers) before storage. This happens transparently inside symmemory itself; SymDesk does not implement a separate content guard.
