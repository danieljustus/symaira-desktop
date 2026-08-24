# Symaira Vault Contract

**contract_version: 5**

This document specifies the format and constraints of a Symaira Vault. Any application or service interacting with the Vault MUST comply with this contract to ensure interoperability across the ecosystem (e.g., `symaira-desktop`, `symaira-ingest`, `symaira-seek`).

> **Backwards compatibility:** Contract v5 is additive. Parsers MUST preserve unknown fields. Vaults using only v1/v2/v3/v4 fields continue to work; the new optional fields are only meaningful for base notes (section 12) and notebook notes (section 10).

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

### Tag Sources and Precedence
Tags can be defined in two locations:
1. **Frontmatter `tags`:** An array of strings (or a single string) in the frontmatter block.
2. **Inline `#tag` markers:** Tags placed directly in the Markdown note body using the `#tag` syntax (including nested tags such as `#project/symaira`).

**Precedence and deduplication rule:**
When indexing or resolving tags for a document, frontmatter `tags` are parsed first. Inline `#tag` occurrences from the note body are appended in order of appearance, deduplicated case-insensitively against frontmatter tags and prior inline tags.

**Inline tag syntax constraints:**
- A tag starts with `#` and contains letters, numbers, underscores, hyphens, and slashes (`/`) for hierarchical tags.
- A tag MUST contain at least one non-numeric character. Bare `#` followed by digits only (e.g. `#123` issue references) is not a tag.
- `#` markers inside fenced code blocks, inline code spans (`` `...` ``), ATX headings (`# Heading`), wikilink targets/fragments (`[[Note#Heading]]`), markdown link targets (`[text](url#section)`), autolinks (`<https://...#frag>`), and bare URLs (`https://example.com/#frag`) MUST NOT be indexed as tags.
- Tag management operations (`symdesk tags rename|merge|delete`) update both frontmatter and inline body occurrences in place without normalizing inline tags into frontmatter.

### Document Kind (contract_version 5)
The `type` field classifies every markdown file in the vault into one of five kinds:

- `note` (default): A free-form note or journal entry.
- `document`: An imported or ingested document with structured metadata (e.g. invoices, letters, contracts).
- `meeting`: A meeting note imported by `symmeet` (see section 8).
- `notebook`: A named, bounded set of vault sources used to scope AI grounding, retrieval and generated artifacts (see section 10). Added in contract_version 4.
- `base`: A saved database view or collection of views over vault documents (see section 12). Added in contract_version 5.

**Explicit declaration:** A file with `type: note`, `type: document`, `type: meeting`, `type: notebook`, or `type: base` in its frontmatter is classified accordingly. Meeting-specific fields (section 8) SHOULD be paired with `type: meeting`; notebook-specific fields (section 10) SHOULD be paired with `type: notebook`; base-specific fields (section 12) SHOULD be paired with `type: base`.

**Inference when absent:** A file with no `type` field is resolved at index time by the following rules (evaluated in order; the first match wins):
1. If the frontmatter contains any of `source_path`, `mime`, `sha256`, `document_date`, or `asn` → the file is classified as `document`.
2. If the frontmatter contains `meeting_id` → the file is classified as `meeting`.
3. If the frontmatter contains `base_id` → the file is classified as `base`.
4. Otherwise → the file is classified as `note`.

`notebook` is never inferred — a `sources` list alone is not a strong enough signal, since a free-form note can legitimately link related files without being a notebook. A notebook note MUST declare `type: notebook` explicitly.

> **Backwards compatibility:** Contract v3 added the `type` field; contract v4 adds the `notebook` kind; contract v5 adds the `base` kind. Existing vaults work without either — every file without `type` is classified at index time by inference, and a vault with no bases or notebooks behaves exactly as a v1/v2/v3 vault always has. Parsers MUST treat an absent `type` as `note` when no inference triggers.

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

## 5. Attachments & Assets
- Attachments (images, PDFs, audio, documents) can be referenced in two ways:
  1. Standard Markdown links `[Title](path/to/file.pdf)` or Markdown image embeds `![Title](path/to/image.png)`.
  2. Wikilink transclusion embeds `![[filename.ext]]` (e.g. `![[scan.png]]`, `![[report.pdf]]`, or with vault-relative path `![[assets/scan.png]]`).
- Attachments can be stored anywhere in the vault, though an `assets/` or `attachments/` subfolder is recommended.
- Embed-style attachment references (`![[filename.ext]]`) resolve against non-Markdown files in the vault by vault-relative path or case-insensitive base name. Missing attachment targets are reported as missing attachments during health scanning.
- **Vault Asset Writer:** Binary assets stored through the Go core (`symdesk asset store`, MCP `desk_asset_store`, `vault.StoreAsset`) or the native app follow shared safety and naming rules:
  - **Folder resolution:** Confined to the vault root; absolute paths and `..` traversal are rejected and fall back safely to `assets`.
  - **Collision-safe naming:** Stored assets use `base.ext`, `base-2.ext`, `base-3.ext`, ... to prevent accidental overwrites.
  - **Base name sanitization:** Path separators (`/`, `\`, `:`), control characters, and newlines in preferred names are replaced with hyphens.
  - **Atomic writes:** Writes use a temp file and atomic rename so partial writes cannot corrupt the vault.

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
  - `contact_ref` (object, optional): an opaque reference to the authoritative `symrelate` contact, set only by an explicit, reviewed link step (`symdesk meeting participant link-contact`). It follows symrelate's published reference-only contract: `provider` (`"symrelate"`), `schema_version` (integer), `id` (stable opaque contact ID), `kind` (`"person"`/`"organization"`), and `display_name` as a refreshable rendering cache — identity is `id` + `kind`. It never contains contact points (email/phone/address/URL/handle), notes, transcript text, or local paths, and unknown additive fields are preserved on round-trip. Linking never creates a contact in `symrelate`; unlinking (`unlink-contact`) removes only the reference. The contact store ships inside SymDesk (nested `relate/` module) and is linked in-process, so the reference schema cannot drift between producer and consumer. Resolution stays optional: with the store unreadable or a contact erased, the note and its stored reference simply keep rendering as-is.
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

## 10. Notebooks (contract_version 4)
A notebook is a named, bounded set of vault sources (`symdesk notebook`, see the CLI help). It is an ordinary Markdown note — there is no separate database and no new source of truth. AI features (ask, the agentic loop, generated artifacts) can be scoped to a notebook so retrieval and citations are bounded to its sources instead of the whole vault.

- `type` (string, `"notebook"`): marks a note as a notebook (section 3). Notes without this field are unaffected by any notebook-specific behavior.
- `notebook_id` (string): a stable identifier for the notebook, generated once at creation and never changed by a rename. Other surfaces (MCP, HTTP API) address a notebook by this ID.
- `sources` (array of strings): vault-relative paths of the files that make up the notebook's scope. A path MUST resolve inside the vault (section 1 sanitization rules apply); paths outside the vault or containing traversal segments MUST be rejected, not silently dropped.
- `description` (string, optional): a short human-readable description of the notebook's purpose.

**Storage convention:** notebook notes live under `notebooks/<slug>.md` at the vault root, where `<slug>` is derived from the title at creation time (mirrors the `meetings/meeting-<id>.md` convention in section 8).

**Source visibility as backlinks:** a notebook's body lists its sources as wikilinks (e.g. `[[invoice-2026-03]]`) under a `## Sources` heading, regenerated from the `sources` frontmatter field on every `symdesk notebook` write. This is the same mechanism the vault already uses for backlinks (section 4) — no new link type is introduced. The `sources` frontmatter field is authoritative; the `## Sources` heading is a derived, human-readable view, the same way `symdesk meeting import` treats the transcript markers in section 8 — hand edits to that section are not read back and are overwritten on the next write.

**Removing a source never deletes the referenced file.** A notebook only references other vault files; it never owns their lifecycle.

> **Backwards compatibility:** Contract v4 adds the `notebook` type and its frontmatter fields. A vault with no notebooks indexes and behaves exactly as a v1/v2/v3 vault always has.

## 11. Delegated Third-Party Formats
SymDesk explicitly recognizes select third-party formats commonly used in Markdown vault ecosystems (e.g. Obsidian). These delegated formats are recognized by vault scanning and entity indexing, but are not interpreted, rendered, or mutated by SymDesk:

- **Obsidian Canvas (`.canvas`):** Whiteboard and canvas files. Wikilinks to existing canvas files (e.g. `[[Board.canvas]]`) resolve as valid vault files and produce no broken link warnings. Canvas rendering and editing remain delegated to Obsidian.
- **Excalidraw Drawings (`*.excalidraw.md`):** Diagram files using the `.excalidraw.md` extension. They remain recognized as vault file entities, but their embedded JSON drawing payloads are excluded from full-text search indexing to prevent search noise.
- **Dataview and Templater Code Blocks:** Fenced code blocks in note bodies (e.g. ```` ```dataview ````, ```` ```templater ````). SymDesk leaves these blocks untouched and does not evaluate them. Wikilinks (`[[…]]`) and tags (`#tag`) within code blocks are ignored during link and tag extraction.

## 12. Bases & Saved Views (contract_version 5)
A base is a named collection of saved views over vault documents (`symdesk views`, see CLI help). Like notebooks and meeting notes, a base is an ordinary Markdown note stored in the vault — Markdown is the single source of truth and there is no hidden database.

- `type` (string, `"base"`): marks a note as a base (section 3).
- `base_id` (string): a stable identifier for the base, generated once at creation and preserved across renames.
- `views` (array of objects): view definitions stored in frontmatter. Each view specifies:
  - `id` (string): stable identifier for the view.
  - `name` (string): human-readable view name.
  - `type` (string): view layout type (`table`, `board`, `calendar`, `gallery`, `timeline`, `list`).
  - `source` (string, optional): document scope (`folder/`, `tag:name`, `notebook:<id>`, or empty for whole vault).
  - `columns` (array of strings, optional): visible property columns.
  - `filters` (array of objects, optional): filter criteria (`key`, `operator`, `value`).
  - `filter_group` (object, optional): recursive all/any condition groups.
  - `sorts` (array of objects, optional): sort ordering (`key`, `ascending`).
  - `group_by` (string, optional): property key to group cards/rows by.
  - `date_property` (string, optional): date property used for calendar/timeline views.
  - `computed` (map of objects, optional): formula and rollup column specifications.
  - `template` (object, optional): note creation template ref and default property values.
- `description` (string, optional): a short human-readable description of the base.

**Storage convention:** base notes live under `bases/<slug>.md` at the vault root, where `<slug>` is derived from the title at creation time (mirrors `notebooks/<slug>.md` in section 10 and `meetings/meeting-<id>.md` in section 8).

**Human-readable view summary:** a base note's body lists its defined views under a `## Views` heading, regenerated from the `views` frontmatter field on every write. The frontmatter `views` definition is authoritative; the `## Views` heading is a derived, human-readable view. Hand edits to that section are overwritten on the next write.

**Indexing and backlinks:** base notes are indexed as first-class vault documents in search and graph view. Wikilinks in base notes resolve in the link graph and backlinks.

**Migration from legacy views:** on startup, existing `.symdesk/views.json` definitions are automatically migrated to base notes in `bases/` while leaving the original `.symdesk/views.json` intact.

