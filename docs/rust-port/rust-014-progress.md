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

## Status at 81525fb0 (RUST-014 NOT passed: macOS local gate open)

Implemented and verified individually on macOS arm64:

- The 11 service tools (`mcp/service_tools.rs`, `mcp/strict.rs`; `vault_timeline` ports Go `Service.Timeline`):
  Go-recorded corpus 640 cases (`testdata/port/mcp/calls-process.json`); the `mcp_calls` replay is green (an
  unfiltered run asserts >= 640 cases executed).
- MCP-002: `tests/mcp_aliases.rs` (`make mcp-aliases`) asserts each alias is byte-identical to its canonical tool.
- SEC-002 MCP part: `tests/mcp_secret_canary.rs` (`make mcp-secret-canary`), planted-leak negative control included.
- `make mcp-conformance` exit 0 (standalone, macOS; includes mcp-protocol-differential, mcp-calls-differential,
  aliases, canary, `symdesk-protocol --test mcp_frame`, mcp_surface/read/write/initialize).
- Fuzz `mcp_frame` for 1800 s: 1,714,862 runs in 1801 s, exit 0, no artifacts.
- `make port-fixtures-check` exit 0 at ff8134e6 after the generator digest re-freeze, the calls-process checksum and a
  both-sides normalization fix (below). `cargo fmt --all --check` clean.

Fixture-harness fixes made here (host-dependent, not port behavior): the CLI-bodies/ingest/paperviews/admin recordings
embedded the recording day's journal filename (`.symdesk/journal/2026-11-01.ndjson`), so they went stale on any other
day; Go and Rust walkers now mask the run-UTC-day journal name (`$DATE`) and skip Finder `.DS_Store`. Fixtures were
regenerated by Go (only that name changed). Main has its own variant of both (bb6d6b1f); the coordinator reconciles.

Two-host gate (`LOCAL_GATE_SLUG=r014-mcp scripts/rust-port/local-gate.sh ... rust-check rust-lint rust-test
port-contract mcp-conformance`, clean tree, SHA 81525fb0674d89f020a6c07d8ac8aa6d302f7d0e):

- Linux arm64: exit 0, receipt `linux-81525fb0-063913.log` (2026-11-05T06:39:13Z-07:53:47Z).
- macOS arm64: exit 2 in all four attempts (`macos-81525fb0-022918/031231/050659/075409.log`). Only host-timing FSEvents
  replays fail: `symdesk-cli::signals signals_match_go` (late/replayed events), `symdesk-cli::sources
  sources_match_go_process_fixture`, and, varying per attempt, `symdesk-index source_tests::external_source_watch...`,
  `symdesk-ingest watcher_contracts`, `symdesk-protocol snapshot_cache_contracts`. Each passes when run alone. Host
  conditions: the shared 1 TB volume hit 100% full twice (OrbStack died twice), load average 15-100 from parallel
  workers, fseventsd at ~95% CPU. The last attempt used `NEXTEST_RETRIES=4 NEXTEST_TEST_THREADS=4`; signals and sources
  still failed 5/5. None of these tests touch MCP code.

Next: rerun `scripts/rust-port/local-gate.sh macos rust-check rust-lint rust-test port-contract mcp-conformance` on a
quiet host (or after main's nextest retry config lands). On exit 0 promote MCP-001/002/003 and the SEC-002 MCP part to
`PASS (local)` naming the SHA, `linux-81525fb0-063913.log` and the new macOS log, then mark RUST-014 passed.

## Closed at `e1b4b0b6` (2026-11-12)

The open macOS receipt is re-taken: two-host local gate at `3b45a118` exit 0 on macOS arm64 (`macos-3b45a118-215436.log`) and Linux arm64 (`linux-3b45a118-215436.log`), targets `rust-check rust-lint rust-test port-contract mcp-conformance http-surface-differential postgres-integration release-contract`. RUST-014 is `passed` in work-items.json and its matrix rows are `PASS (local)`.
