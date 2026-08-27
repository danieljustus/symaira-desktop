# MCP Migration Matrix — SymIngest & SymSeek → symdesk

> Last updated: 2026-08-25 · Covers the absorbed SymIngest (13 tools) and
> SymSeek (12 tools) MCP contracts against the unified `symdesk mcp`
> surface. This file is the checked-in source of truth for the migration
> path (issue #598).

## How to read this matrix

- **Alias** — `symdesk mcp` serves the legacy tool name with a compatible
  request/response schema, delegating to the exact same handler as the
  canonical tool. No client change needed beyond pointing at `symdesk mcp`.
- **Sidecar (CLI)** — the capability exists in `symdesk`, but only as a
  CLI command, not as an MCP tool. Migrate the workflow to the listed
  command; an MCP tool is intentionally not exposed.
- **Successor** — no 1:1 contract, but a semantic successor exists. The
  workflow migrates to the successor; schemas differ.
- **None** — `symdesk` does not provide this capability. Use the listed
  workflow instead; do not expect the old tool to be served.

## symingest (13 tools)

| Legacy tool        | Status        | symdesk equivalent                       | Notes |
|--------------------|---------------|------------------------------------------|-------|
| `ingest_file`      | **Alias**     | `desk_ingest`                            | Same semantics: ingest a file into the vault (copies to `inbox/` + note). |
| `list_jobs`        | **Alias**     | `desk_ingest_jobs`                       | Lists the ingestion job queue. |
| `retry_job`        | **Alias**     | `desk_ingest_retry`                      | Retries a failed job by ID. |
| `import_paperless` | Sidecar (CLI) | `symdesk paperless import <export-dir>`  | Paperless-ngx export migration; CLI command only. |
| `add_rule`         | **Alias**     | `desk_rules_add`                         | Adds a classification rule (pattern/kind/value); gated behind the mutation gate like the other write tools. |
| `delete_rule`      | **Alias**     | `desk_rules_delete`                      | Deletes a classification rule by ID. |
| `list_rules`       | **Alias**     | `desk_rules_list`                        | Lists the configured classification rules. |
| `merge_pdf`        | **Alias**     | `desk_merge_pdf`                         | Merges two or more PDFs via `pdfunite` (also `symdesk ingest merge`). Requires Poppler on PATH. |
| `reocr`            | **Alias**     | `desk_ingest_reocr`                      | Re-runs OCR/extraction for an already-ingested document by ID or archived path (also `symdesk ingest reocr`). |
| `rotate_pdf`       | **Alias**     | `desk_rotate_pdf`                        | Rotates pages by 90° multiples via `qpdf` (also `symdesk ingest rotate`). Requires qpdf on PATH. |
| `split_pdf`        | **Alias**     | `desk_split_pdf`                         | Splits a PDF after given pages via Poppler (also `symdesk ingest split`). Requires Poppler on PATH. |
| `start_watch`      | None          | `symdesk events` (+ `symdesk consume set-path`) | Decision (issue #637): no MCP start/stop control. The inbox watcher runs inside the long-lived `symdesk events` process; configure the watched folder with `symdesk consume set-path <dir>` and check it with `symdesk consume status`. `start_watch`/`stop_watch` are dropped. |
| `stop_watch`       | None          | `symdesk events` (Ctrl-C / stdin close)  | ditto — the watcher stops with the `symdesk events` process. |

## symseek (12 tools)

| Legacy tool                  | Status        | symdesk equivalent            | Notes |
|------------------------------|---------------|-------------------------------|-------|
| `search_documents`           | **Alias**     | `desk_search`                 | Full-text search over vault content with the same `query` argument. |
| `list_documents`             | **Alias**     | `desk_docs`                   | Lists indexed documents (adds filters; superset of the legacy list). |
| `index_document`             | Sidecar (CLI) | `symdesk index [path]`        | Index a single document into the sidecar. |
| `read_document`              | Sidecar (CLI) | `symdesk get [file]`          | `get` returns document properties; read the note body via the vault file itself or `desk_search`. |
| `get_context`                | Successor     | `notebook_get` / `notebook_ask` | Legacy folder-context → notebook (named bounded source set). Schemas differ. |
| `get_contexts`               | Successor     | `notebook_list`               | List contexts → list notebooks. |
| `set_context`                | Successor     | `notebook_create` + `notebook_add_source` | Create a bounded context for scoped AI grounding. |
| `index_url`                  | Successor     | `desk_clip`                   | URL → vault note (clip saves a note; it does not index into the sidecar). |
| `get_document_extractions`   | None          | —                             | Structured field extractions are not a separate retrievable store; use `desk_docs` (metadata) + `desk_props` (frontmatter). |
| `list_extractions`           | None          | —                             | ditto |
| `search_extractions`         | None          | —                             | ditto |
| `multi_get`                  | None          | —                             | No multi-document read tool. Loop `desk_docs`/`desk_search` per document. |

## Summary

25 legacy tools → **12 aliases**, **3 sidecar (CLI)**, **4 successors**,
**6 none**.

## Migrating the Hermes configuration

Hermes currently registers the legacy binaries:

```bash
hermes mcp list            # shows symingest + symseek entries
```

### Switch

1. Install/update `symdesk` (Homebrew: `brew install danieljustus/tap/symdesk`).
2. Remove the legacy servers:
   ```bash
   hermes mcp remove symingest
   hermes mcp remove symseek
   ```
3. Add the unified server (once):
   ```bash
   hermes mcp add symdesk --command "symdesk mcp"
   ```
4. In the Hermes profile config, replace any tool references that used the
   legacy names (`ingest_file`, `search_documents`, …) with the alias names
   — the aliases are served by `symdesk mcp` with compatible schemas, so
   only the server URL/command changes.

### Removing the disabled `symingest` sidecar (issue #637)

The `symingest` Homebrew formula has been `disable!`d since 2026-08-24. Once
the matrix above is verified, uninstall the binary:

```bash
brew uninstall danieljustus/tap/symingest
hermes mcp remove symingest   # if still registered
```

Nothing in the matrix depends on it any more: every tool it served is either
served by `symdesk mcp` directly (aliases), available as a `symdesk` CLI
command (sidecar), replaced by a successor, or deliberately dropped with a
documented replacement (`start_watch`/`stop_watch` → `symdesk events`).

### Verification

```bash
# The legacy names must appear in tools/list:
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | symdesk mcp | grep -E 'ingest_file|search_documents|list_jobs|retry_job|list_documents'
# Spot-check one legacy call:
echo '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_jobs","arguments":{}}}' | symdesk mcp
```

### Rollback

Re-install the archived formulas only if you still have a working bottle:

```bash
brew install symingest symseek
hermes mcp remove symdesk
hermes mcp add symingest --command "symingest mcp"
hermes mcp add symseek --command "symseek serve"
```

> The archived formulas are **disabled**, not deleted — reinstall works from
> a local bottle or by building from the archived source. After the migration
> is verified, uninstall them to remove the operational dependency.

### Out of scope

- Reintroducing separate `symingest` / `symseek` product repos or binaries.
- Renaming canonical `desk_*` tools for cosmetic parity.
- Faking any of the 14 "none" capabilities with schema-incompatible adapters.
