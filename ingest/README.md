# symaira-ingest (`symingest`)

> **Dieses Verzeichnis war bis 2026-08-23 das eigenständige Repository
> [`symaira-ingest`](https://github.com/danieljustus/symaira-ingest).** Die Go-Implementierung
> ist nun direkt in das Root-Modul von [`symaira-desktop`](../README.md) unter `internal/ingest`
> aufgelöst (Issue #540 Migration). Die OCR-/Ingest-Pipeline wird über
> `symdesk ingest` genutzt.
> Die Badges, Installations- und Release-Hinweise unten stammen aus der
> Zeit als eigenes Repo und gelten nicht mehr.
>
> Dieses Verzeichnis dient als historische Ablage für Dokumentation (`docs/`), Packaging-Skripte (`scripts/`),
> Standalone-Client-Referenz (`client/`) und Marken-Assets (`assets/`).

[![CI](https://github.com/danieljustus/symaira-ingest/actions/workflows/ci.yml/badge.svg)](https://github.com/danieljustus/symaira-ingest/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

![Symaira Ingest social preview](docs/assets/social-preview.png)

Document ingestion + OCR core for the [Symaira](https://github.com/danieljustus?tab=repositories&q=symaira) ecosystem.

Drop a scanned PDF, image, or text-like export into a folder → get a searchable, classified **Markdown** note out. Think of it as a Paperless-ngx "consume" pipeline that emits plain Markdown + YAML frontmatter instead of a proprietary archive.

## What is this / Why use it?

- **Standalone CLI + MCP server** — no external services required, runs entirely on your machine
- **OCR for scanned documents** — extracts text from PDFs and images using Tesseract (`pdf`, `png`, `jpeg`, `tiff`, `webp`, plus `heic/heif` on macOS via `sips`)
- **Text and structured imports** — preserves plain text, Markdown, CSV, HTML, RTF, DOCX, XLSX, ODT and EML without forcing them through OCR
- **Markdown output** — produces clean, searchable Markdown with YAML frontmatter instead of proprietary formats
- **MCP integration** — works as an MCP tool for AI-powered document processing workflows
- **Classification rules** — automatically categorize documents based on content patterns
- **Paperless-ngx import** — pull documents from an existing Paperless-ngx instance, preserving tags, correspondent and document type as frontmatter

## Status

**Beta** (pre-1.0 SemVer). The CLI + MCP pipeline is stable and covered by CI on every commit; flags and output formats may still evolve between minor releases. Paperless-ngx replacement readiness is tracked in [`docs/plans/symingest-completion-roadmap.md`](docs/plans/symingest-completion-roadmap.md).

## Install

**Homebrew (recommended for macOS):**

```bash
brew install danieljustus/tap/symdesk             # enthält die Ingest-Pipeline
brew install --cask danieljustus/tap/symdesk-app  # macOS GUI app (DMG)
```

**Go install:**

```bash
go install github.com/danieljustus/symaira-ingest/cmd/symingest@latest
```

**Prerequisites:**
- Go 1.26.4+
- `tesseract` (for OCR)
- `pdftoppm` (for PDF rendering)
- Poppler `pdfinfo`, `pdfseparate` and `pdfunite` (for PDF split/merge)
- `qpdf` (optional; required for PDF page rotation)
- `magika` (optional; content-type verification via google/magika; warns on extension/MIME mismatch during file type detection)
- `sips` on macOS for direct HEIC/HEIF OCR when Paperless has no archived PDF rendition

## Usage

**Ingest a single file:**

```bash
symingest ingest /path/to/document.pdf
```

```text
$ symingest ingest -vault ~/vault -db ~/.local/share/symingest/jobs.db invoice.txt
ingested: invoice.txt
engine: text
text length: 71
```

The resulting note in your vault carries YAML frontmatter plus the extracted text:

```text
---
source_path: invoice.txt
ingested_at: 2026-06-30T22:24:49.542837Z
sha256: 39f4280386fd5df04e0e06d7d7fa1c5a2aaaa54b643e92ae9c859c0c6f1117d6
mime: text/plain
tags: []
category: ""
ocr_engine: text
archive_path: ~/.local/share/symingest/archive/39f4280386fd5df04e0e06d7d7fa1c5a2aaaa54b643e92ae9c859c0c6f1117d6.txt
---

INVOICE #4471
Acme Hardware Supply
Date: 2026-03-12
Total Due: $284.50

---
[Archived Original](file:///.../archive/39f4280386fd5df04e0e06d7d7fa1c5a2aaaa54b643e92ae9c859c0c6f1117d6.txt)
```

**Re-run OCR for an already-ingested document:**

```bash
symingest reocr --document-id 123 --lang deu+eng
symingest reocr /path/to/archived-original.pdf --json
```

`reocr` reads the archived original, validates its SHA-256 against the document store, and updates the existing Markdown note in place. Machine-owned frontmatter is refreshed, while user-added frontmatter fields and the note path are preserved. The command returns a non-zero exit status with an explicit error when the original is missing, unregistered, or has changed. The same operation is available through the MCP tool `reocr` with either `document_id` or the registered archived `source` path.

**Repair PDF page structure:**

```bash
symingest split --at 2,4 --output-dir ./parts scan.pdf
symingest merge --output merged.pdf ./parts/part-001.pdf ./parts/part-002.pdf
symingest rotate --degrees 90 --pages 1,3 --output rotated.pdf scan.pdf
```

`split` and `merge` use Poppler and never modify their inputs. `rotate` uses qpdf and is unavailable with an explicit error when qpdf is not installed. Add `--ingest` to any operation to pass the generated PDFs through the normal OCR/classification pipeline; originals remain untouched. The MCP tools `split_pdf`, `merge_pdf`, and `rotate_pdf` expose the same operations to agents.

**Extract structured data from a file:**

```bash
symingest extract /path/to/document.pdf --profile generic --json
```

```text
$ symingest extract invoice.txt --profile invoice
Profile: invoice
File: invoice.txt
Text length: 71
Extractions: 4

  date:                2026-03-12 [12:22]
  amount:              $284.50 [45:53]
  invoice_number:      INV-4471 [0:10]
  email:               accounts@acme.com [55:72]
```

With `--json`, outputs JSONL (one JSON object per line) for programmatic consumption. Available profiles: `generic` (dates, amounts, emails, URLs, IBANs), `invoice` (adds invoice numbers, totals, due dates), `contract` (adds parties, deadlines), `jobcenter` (adds job IDs, appointment dates).

During normal ingestion, if an extraction profile is configured, sidecar JSONL files are written under `.symaira/extractions/<sha256>.jsonl` in the vault and the note frontmatter includes `sidecar_path` and `extraction_count`. Extraction failures are logged but never roll back a completed ingest.

Generated vault notes are private by default: note files are written with `0600` permissions and newly created vault subdirectories with `0700`, matching the archive and database defaults.

**Watch a directory for new files:**

```bash
symingest watch \
  --processing-dir /path/to/inbox/.processing \
  --processed-dir /path/to/inbox/.processed \
  --failed-dir /path/to/inbox/.failed \
  /path/to/inbox
```

The watcher waits for files to become stable, moves work through processing/processed/failed folders when configured, writes `.error.json` sidecars for failed files, resets stale running jobs on startup, and refuses a duplicate watcher for the same inbox/database lock.

**macOS LaunchAgent service:**

```bash
symingest service --dry-run install     # print plist, write nothing
symingest service install               # write ~/Library/LaunchAgents/dev.symaira.symingest.watch.plist
symingest service start
symingest service --json status
symingest service --lines 200 logs
symingest service stop
symingest service uninstall
```

The generated LaunchAgent embeds paths only, never Paperless tokens or other secrets. Service logs are written to `~/Library/Logs/symingest/watch.log` and `watch.err.log`.

**MCP server mode:**

```bash
symingest mcp
```

**Mail ingestion:**

`symingest watch` also polls configured IMAP mailboxes when accounts are set up in the config file (`~/.config/symingest/config.toml` or `./.symingest.toml`):

```toml
imap_poll_interval = "5m"

[[imap_accounts]]
host            = "imap.example.com"
port            = 993
username        = "invoices@example.com"
password_secret = "symvault://imap/invoices"   # or "env://IMAP_PASSWORD" or "keychain://symingest/invoices"
# Bare values are treated as environment-variable names, not literal passwords.
folder          = "INBOX"
from            = ["billing@vendor.com"]        # optional sender filter
subject         = ["Invoice"]                   # optional subject filter
has_attachment  = true                          # only consider messages with attachments
action          = "mark_seen"                   # "mark_seen" or "move"
move_to         = "Processed"                   # required when action = "move"
archive_mail    = false                         # also ingest the .eml message body itself
```

Each matching attachment is ingested through the normal pipeline (OCR, extraction, classification) exactly like a file dropped into the watched inbox, and appears in `symingest jobs` with the same retry semantics as filesystem-sourced jobs. Messages are tracked by Message-ID, so a mailbox is never re-ingested on the next poll. `password_secret` supports `symvault://`, `env://`, and `keychain://` references; a bare value is interpreted as an environment-variable name and fails clearly when that variable is unset. Connection or authentication failures for an account are surfaced by `symingest doctor` and logged on each poll attempt.

**Manage classification rules:**

```bash
symingest rules list
symingest rules add "invoice" category Finance
```

Rule patterns are case-insensitive substrings matched against extracted document text. They are not filename globs, so a pattern like `*.pdf` will only match literal text in a document, not PDF filenames.

For integrations, pass `--json` before the rules subcommand. All successful rules JSON responses use `schema_version: 1` and stable operation-specific envelopes:

- `list`: `{ "schema_version": 1, "rules": [...] }`
- `add` / `update`: `{ "schema_version": 1, "rule": { ... } }`
- `test`: `{ "schema_version": 1, "matches": [...] }`
- `delete`: `{ "schema_version": 1, "id": 123, "deleted": true }`

The rule objects contain `id`, `pattern`, `kind`, `value`, and `created_at`. A successful empty list or test returns an empty JSON array rather than `null`. Human-readable output remains unchanged when `--json` is omitted.

**Check job queue:**

```bash
symingest jobs
symingest retry <job-id>
```

**Import from a Paperless-ngx instance:**

```bash
symingest import paperless --base-url https://paperless.example.com --token <api-token> --vault ~/vault
```

```text
Flags:
  --base-url string   Paperless-ngx instance URL (or PAPERLESS_URL env)
  --token string      API token, or PAPERLESS_TOKEN env; also accepts
                      keychain://service/account or symvault://ref
  --since string      Only import documents whose Paperless created date is on
                      or after this date (YYYY-MM-DD)
  --limit int         Import at most N documents (newest first); 0 means no limit
  --ids string        Import only these Paperless document IDs (comma-separated,
                      e.g. 123,456); takes precedence over --since and --limit
  --preserve-storage-paths
                      Place notes under vault subdirectories derived from each
                      document's Paperless storage path (default: flat layout)
  --vault string      Target vault directory
  --archive string    Target archive directory
  --db string         SQLite database path
  --dry-run           List what would be imported without writing
  --report string     Write a JSON migration report to this path (works with
                      --dry-run and real imports)
  --verify            Verify a completed import against the Paperless source
                      (compares notes, archived originals, and metadata), then exit
  --status            List per-document import status from a previous run, then exit
  --json              With --status or --verify, output the result as JSON
```

`--report <path>` writes a stable JSON migration report for a dry-run or a real import: `schema_version`, `tool_version`, overall counts plus a per-document array (`id`, `status`, optional `reason`, and generated `vault_path`/`archive_path`/`sha256`), collected warnings, and — for a dry-run — the unsupported file types and unresolved metadata IDs from the audit. Like every other output it contains no document content, so it is safe to hand to a review step or a later UI. A real import exits non-zero if any document fails; re-run the same command or use `--retry-failed` until `failed: 0`.

After an import, `--verify` re-reads the Paperless source and the generated vault notes and reports any document that is missing, duplicated, missing its archived original, or whose metadata (tags, correspondent, document type, storage path, created date) drifted from the source. It prints a human summary, or a stable JSON report with `--json`, and exits non-zero when any discrepancy is found — suitable as an automated migration gate before Paperless is retired. Only IDs, field names, and paths appear in the output; document content never does.

**Import from a Notion export:**

```bash
symingest import notion ~/Downloads/Notion-Export
```

Flags:

```text
  --vault string         Target vault directory
  --dry-run              List what would be imported without writing
  --import-run-id string Idempotency key; re-running with the same ID skips
                         already-imported notes
  --report string        Write a JSON migration report to this path
```

Export your Notion workspace as **Markdown + CSV** and unzip the archive. Then point `symingest import notion` at the top-level export directory. The importer converts:

- Notion pages into Markdown notes with YAML frontmatter
- CSV databases into one note per row, with CSV columns as top-level frontmatter
  properties, plus an index note per database
- Page links into `[[...]]` wikilinks using clean, unique note names
- Attachments and images into `assets/` with relative links rewritten to
  content-addressed filenames
- Nested export folders into matching vault subdirectories

Use `--dry-run` to preview the import without writing anything. Use `--import-run-id` when running the command repeatedly; notes already recorded under that run ID are skipped. `--report <path>` writes a stable JSON migration report (schema version, tool version, counts, and per-note results) that contains no document content and is safe to hand to a review step.

**Gate Paperless cutover:**

```bash
symingest search index ~/vault

cat > search-fixtures.json <<'JSON'
[
  {"query":"sample invoice token", "min_results":1, "must_contain":["invoice"]}
]
JSON

symingest search validate \
  --fixtures search-fixtures.json \
  --report search-report.json
```

Search integration is optional and runtime-detected. To index each generated note after a successful ingest, enable it in config:

```toml
symseek_enabled = true
symseek_binary = "/opt/homebrew/bin/symseek" # optional; PATH lookup is default
```

Post-ingest indexing failures are logged but do not roll back a completed ingest. Use `symingest search index` and `symingest search validate` as explicit migration evidence; search reports contain no document bodies.

```bash
symingest cutover-check \
  --dry-run-report dryrun-report.json \
  --import-report import-report.json \
  --verify-report verify-report.json \
  --search-report search-report.json \
  --vault ~/vault \
  --min-documents 6000 \
  --min-body-length 40
```

For maximum source fidelity, run verification in deep mode before the cutover check:

```bash
symingest import paperless --verify --deep --json > verify-report.json
```

`--deep` re-downloads each selected Paperless original and compares its SHA-256 with the archived original in the vault. It is slower by design and belongs in the final migration gate, not every quick local check.

`cutover-check` is intentionally strict: Paperless stays the source of truth unless the full dry-run, real import, verifier output, search validation, and vault validation are all clean and the document counts agree. Use `--json` for CI or app integration.

Validate machine-readable report files before using them as cutover evidence:

```bash
symingest report validate dryrun-report.json
symingest report --json validate verify-report.json
symingest report --json validate search-report.json
```

Create a body-safe review surface and apply explicit corrections with count gates:

```bash
symingest review-report --failed --warnings --unsupported --unresolved --html review.html import-report.json
symingest review-report --duplicate-content --json verify-report.json
symingest apply-corrections --vault ~/vault --dry-run --require-count 3 corrections.yaml
symingest apply-corrections --vault ~/vault --require-count 3 --max 3 --backup-dir undo corrections.yaml
symingest bulk-update --vault ~/vault --where tag:needs-review --add-tag reviewed --dry-run --require-count 12 --max 12
```

`corrections.yaml` supports the versioned shape:

```yaml
schema_version: 1
corrections:
  - paperless_id: 123
    add_tags: [reviewed]
    correspondent: Example GmbH
```

The macOS app exposes these same migration surfaces: Paperless import/verify/status, migration review, corrections dry-run/final apply, frontmatter/original preview, rules create/edit/delete/test, jobs retry/error-sidecar reveal, watcher service controls, and manual symseek vault indexing. Paperless tokens are entered through `SecureField` and stored in Keychain; the generated config file never contains the token.

For OCR quality checks, add a body-length gate to vault validation:

```bash
symingest validate-vault --min-body-length 40 --json ~/vault
```

This fails notes with empty or near-empty Markdown bodies, catching scanned documents where OCR technically ran but produced no useful text.

`--since` filters on the document's Paperless *created* date (the date shown on the document), not the date it was added to Paperless. Use `--limit` or `--ids` to run a small, inspectable pilot before a full migration; both bounds apply to `--dry-run` and real imports alike, and a bounded run echoes the selected document IDs. Imports are resumable: a document already recorded as imported is skipped on a re-run, and a document that previously failed is retried automatically. With `--preserve-storage-paths`, each note is placed under a vault subdirectory derived from the document's Paperless storage path instead of the vault root; unsafe path segments are sanitized and collisions are resolved deterministically. Also available as the `import_paperless` MCP tool, which accepts the same options (`base_url`, `token`, `since`, `dry_run`, `limit`, `ids`, `preserve_storage_paths`, `report_path`, plus optional `vault_path`/`archive_path`/`db_path` overrides).

For a complete, gated migration path — dry-run, bounded pilot, full import, verification, and search validation, with an explicit rule for when Paperless can stop being the source of truth — follow the [Paperless replacement runbook](docs/paperless-replacement-runbook.md).

## Development

**Build:**

```bash
go build ./cmd/symingest
```

**Run tests:**

```bash
go test ./...
```

**Run linter:**

```bash
go vet ./...
```

## Architecture

`symingest` is part of the Symaira ecosystem:

- **symingest** (this repo) — Document ingestion + OCR
- **[symseek](https://github.com/danieljustus/symaira-seek)** — Search and retrieval
- **[symdesk](https://github.com/danieljustus/symaira-desktop)** — Desktop shell

> Design rule: `symingest` writes Markdown + frontmatter into the vault. Search remains `symseek`'s job; `symingest` only shells out to `symseek` for optional post-ingest indexing and machine-readable validation evidence.

## License

Apache-2.0
