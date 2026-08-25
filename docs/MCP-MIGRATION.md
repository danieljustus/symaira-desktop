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
| `add_rule`         | None          | —                                        | No rule-management API in `symdesk`. Use `doc_set_status` / `desk_docs` filters for the same workflow. |
| `delete_rule`      | None          | —                                        | ditto |
| `list_rules`       | None          | —                                        | ditto |
| `merge_pdf`        | None          | —                                        | No in-process PDF merge. Merge externally, then `desk_ingest`. |
| `reocr`            | None          | —                                        | No re-OCR command. Re-run `desk_ingest` on the source file to re-extract. |
| `rotate_pdf`       | None          | —                                        | No in-process PDF rotate. Rotate externally, then `desk_ingest`. |
| `split_pdf`        | None          | —                                        | Split exists in-process (`ingest.SplitPDFAtSpec`) but is not exposed as CLI/MCP. Use an external splitter, then `desk_ingest`. |
| `start_watch`      | None          | —                                        | The internal watcher has no public CLI/MCP surface. Use `desk_ingest` per file or the self-hosted server's watch. |
| `stop_watch`       | None          | —                                        | ditto |

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

25 legacy tools → **5 aliases**, **2 sidecar (CLI)**, **4 successors**,
**14 none**.

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
