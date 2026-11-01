# RUST-014 progress (branch `local/r014-mcp`, base 63f1189b)

Worker note for a successor. Scope: MCP-001, MCP-002, MCP-003 and the MCP failure paths of SEC-002 for the
SymDesk MCP server (MCP-005 SymRoom and SEC-003 already PASS locally). Oracle: the real Go `symdesk mcp`
process (corekit `mcpserver` v0.18.3-0.20261012110002) in a disposable HOME/XDG/vault.

## Inventory (start of RUST-014)

| Area | State at 63f1189b |
|---|---|
| Transport (framing, JSON syntax, envelope) | Approximation in `crates/symdesk-cli/src/mcp.rs`; error texts and several framing edge cases differ from Go |
| `initialize` | `instructions` is **absent** in Go (`StartServer` never calls `SetInstructions`), so "nonempty instructions" in the work-item wording cannot be a parity requirement: absence is the Go contract and is pinned |
| Catalog (`tools/list`) | Byte-exact for both registries (`tests/mcp_surface.rs`, 29 read-only / 57 read-write, 4 raw transcripts) |
| Native tool handlers | 31 of 57: desk_status, desk_ls, desk_search (+`search_documents`), desk_ask, desk_props, desk_backlinks, desk_diagram, desk_read_result, notebook_*, meeting_*, desk_docs/docs_review/docs_similar (`read/extra.rs`), 7 write tools, 3 PDF tools (+3 aliases) |
| Missing handlers (26 names) | vault_timeline, desk_transform, vault_health, desk_related, desk_ingest_jobs, desk_rules_list, desk_dataset_list/describe/query/sync, desk_ingest, desk_ingest_retry, desk_ingest_reocr, desk_rules_add/update/delete, desk_clip, desk_export, desk_autofill and the aliases ingest_file, list_jobs, retry_job, reocr, list_rules, add_rule, delete_rule |

## Done

1. **MCP-001 transport** — `symdesk_protocol::mcp_frame` is a port of the Go corekit transport (line and
   `Content-Length` framing, header limits, the `encoding/json` syntax scanner so `Parse error:` texts are
   Go's, request envelope with raw `json.Number` ids, response rendering). It is a copy of the passing SymRoom
   port (`symroom-cli/src/mcp.rs`, untouched) lifted into a pure module; `symdesk-cli/src/mcp.rs` now reads
   requests through it, echoes ids as raw JSON text and prints fatal read errors as Go does
   (`mcpserver: read error: ...`, no `mcp:` prefix). `initialize` result keys are in Go's sorted order.
   Recorded by `cmd/symdesk/mcp_protocol_port_test.go` (`make mcp-protocol-fixtures-generate`) into
   `testdata/port/mcp/protocol-process.json` (334 cases: id types, params shapes, jsonrpc/method variants,
   notifications, malformed JSON, line/CRLF/NUL/BOM streams, 70+ `Content-Length` frame cases, 1 MiB boundary
   sizes, depth limit, non-UTF-8 inputs). Replays: `symdesk-cli/tests/mcp_protocol.rs` (every case against the
   native binary, `make mcp-protocol-differential`) and `symdesk-protocol/tests/mcp_frame.rs` (pure transport,
   Go-eligible transcripts byte-for-byte, frame well-formedness, 3,000 seeded mutations). Fuzz target
   `fuzz/fuzz_targets/mcp_frame.rs` with 320 named seeds in `fuzz/seeds/mcp_frame`.

## Remaining

- Handlers for the 26 missing tool names with Go-recorded calls (success/invalid/write-disabled/backend).
- Alias and registry assertions (MCP-002) beyond the catalog bytes: alias-vs-canonical call equivalence.
- SEC-002 MCP canary corpus.
- `make mcp-conformance`, bounded `mcp_frame` fuzz (1800 s), full two-host local gate, matrix/work-items.

## Last checks

See the commit messages and the table below (filled as slices land).
