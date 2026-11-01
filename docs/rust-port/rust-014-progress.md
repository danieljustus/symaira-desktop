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

## Done since (branch rebased on main 7145e89b, no rebase in progress)

2. **Ingest-store tools** (rules, ingest, jobs, retry, reocr + 7 aliases): Go-recorded in
   `testdata/port/mcp/calls-process.json` (393 cases). Root cause of the earlier 35 "databases differ"
   failures: sidecar `files` rows hash/size/simhash notes that embed clock readings and the root path.
   Fix (both sides): `sha256/size/simhash` of a `files` row are recomputed from the root/clock-normalized
   file content; a row whose indexed SHA no longer matches the file on disk (5 cases: reocr-01/02/12,
   no-vault-desk_ingest, ingest-framed; Go rewrites the note after indexing) is recorded as `"<STALE>"`.
   Helper names verified: `symdesk_vault::sha256_hex`, `mcpCallsNormalize`, `dump_db(path, root)`.
   Check: `make mcp-calls-differential` exit 0 (Go oracle ok 10.9 s; `cargo test -p symdesk-cli --test mcp_calls`
   3 passed, 77.6 s) at commit "test(mcp): pin ingest call fixtures ...".

## Uncommitted-at-session-end (WIP checkpoint)

- `cmd/symdesk/mcp_calls_service_port_test.go` (new, ~170 cases) and its hook in `mcpCallsAllCases`:
  Go corpus for the 11 still-missing tools. **Not yet generating**: `make mcp-calls-fixtures-generate` fails
  because the seeded `broken.md` (invalid YAML) is indexed by `mcpCallsIndexVault` via `vault.ParseBytes`
  (`health-broken-yaml` case). Fix: exclude `broken.md` from indexing (add an `IndexSkip` set or seed it as a
  RootFile `vault/broken.md`). Until fixed the Go test fails; `calls-process.json` is still the 393-case
  fixture from the last passing run, and the Rust replay will fail once regenerated until the handlers exist.

## Remaining

- Fix the corpus above, generate, then implement the Rust handlers (new `mcp/service_tools.rs`, dispatch in
  `mcp.rs::dispatch_tool_call`) for: vault_timeline (needs a port of Go `Service.Timeline`: journal ReadRange
  with flexible `ts`, `Sidecar` TimelineDocs, meetings, `symdesk_vault::retention::load_history`,
  `HistoryStore::trash_list`, stable sort + dedupe), desk_transform (reuse `ai_cli.rs` helpers, collect chunks
  into `{"result":...}`; empty text -> "text is required"), vault_health (`vault_health_cli::scan`, add
  `Kind::Int` for Go `int` in `mcp/args.rs`), desk_related (`related_cli` body), desk_dataset_list/describe/
  query/sync (`dataset.rs`; Go uses DisallowUnknownFields + `dataset is required`), desk_clip
  (`clip_cli::execute`), desk_export (`export_cli::export`), desk_autofill (`admin_ai_cli::autofill`).
  Read-only ones: timeline, transform, health, related, dataset_list/describe/query; write: dataset_sync, clip,
  export, autofill.
- MCP-002 alias-vs-canonical equivalence assertions; SEC-002 MCP canary corpus (a transform canary case with
  `SYMDESK_LLM_API_KEY` is already in the new corpus).
- `make mcp-conformance`, `cargo nextest run -p symdesk-protocol`, `cargo +nightly fuzz run mcp_frame
  -- -max_total_time=1800`, `dev-external make port-fixtures-check` (generator_source_digest re-freeze),
  two-host `local-gate.sh` (macOS arm64 + Linux arm64), matrix/work-items rows. None of these ran this session.

## Last checks

- `make mcp-calls-differential` PASS (see item 2). No other gate was run on this branch after the rebase.
- RUST-014 is NOT passed: MCP-001 transport and MCP-003 for rules/ingest are verified; MCP-002/003 for the
  11 tools above, SEC-002 canary, fuzz and the two-host gate are open.
