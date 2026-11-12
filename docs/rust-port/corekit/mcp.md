# CK-MCP: `symaira-core-mcp`, `symaira-core-mcpcfg`, `symaira-core-update`

Decision record for the three CoreKit MCP-adjacent crates. Base `f24377a4`,
CoreKit revision `592bebb54080d6b37372e47c461cfa3aeca7d2af` (the revision every
CoreKit crate in this workspace is pinned to).

## Verdict

| Site | Crate | Verdict |
|---|---|---|
| `symroom mcp` (`crates/symroom-cli/src/mcp.rs`) | `symaira-core-mcp` | **Not adopted.** 49 of 341 replayed Go-recorded steps differ (43 still differ with CoreKit's own `serde_json` features), plus the in-flight cap. |
| `symdesk mcp` (`crates/symdesk-cli/src/mcp.rs`, `mcp/`, `symdesk-protocol::mcp_frame`) | `symaira-core-mcp` | **Not adopted.** 62 of 334 recorded raw transcripts and 4 of 4 `tools/list` recordings differ in this workspace; 28 still differ with CoreKit's own `serde_json` features. |
| MCP client-config discovery or installation | `symaira-core-mcpcfg` | **No use site** in the Rust tree, none in the Go tree at `36aad3d4`. |
| Self-update | `symaira-core-update` | **No use site** in the Rust tree, none in the Go tree at `36aad3d4`. |

Both local servers keep their private framing, validation and dispatch code.
No product code changed. What was added is a dev-dependency on
`symaira-core-mcp` (`[workspace.dependencies]` entry plus
`[dev-dependencies]` of `symdesk-cli` and `symroom-cli`) and two tripwire
tests, `crates/symdesk-cli/tests/corekit_mcp_compare.rs` and
`crates/symroom-cli/tests/corekit_mcp_compare.rs`. Each drives
`Server::serve_io` with the raw bytes of the frozen fixtures and asserts the
exact set of diverging cases, so a CoreKit fix makes the test fail and forces a
re-evaluation of this page.

## The question the earlier finding left open

`corekit-adoption.md` ("MCP slice") deferred adoption without saying which side
agrees with the Go recordings. Both Go servers were built entirely on the Go
package `corekit/mcpserver` at `v0.18.3-0.20261012110002-27d2daf8ec1e`
(`internal/mcp/server.go`: `mcpserver.New`, `RegisterTool`, `ServeStdio`;
`internal/room/mcp/server.go`: `mcpserver.New`, `SetInstructions`,
`RegisterTool`). Framing, validation, dispatch and encoding were therefore the
Go library's, so the frozen fixtures record that library's behaviour.
**Where the local Rust code and CoreKit's Rust port differ, the local code
matches the recordings** (the replays below are green on it) and the CoreKit
crate does not.

Two upstream facts explain most of it:

1. The Rust crate's Go oracle is corekit commit `ff0e10ed` (contract rows
   MCP-001..012). The desktop recordings were made against `27d2daf8`, which
   adds, among others, "keep serving after a parse or invalid-request error"
   (#411), "report cancellation that arrives while handlers drain" (#413),
   "interrupt inherited stdin on stdio write failure" and the `stdio_unix.go`
   stdin duplication. The crate follows the older oracle.
2. Go's `encoding/json` decodes into structs case-insensitively, replaces
   invalid UTF-8 and lone surrogates with U+FFFD, keeps number tokens verbatim
   in `json.RawMessage`, and allows 10000 levels of nesting. The Rust crate
   parses through `serde_json::Value`, which does none of these.

## Evidence

Method: `Server::serve_io` of `symaira-core-mcp` with stub handlers that replay
the recorded tool text (symroom) or a constant (symdesk), so only transport,
framing, validation and encoding are compared. Server name, version,
instructions and tool inventory come from `testdata/port/mcp/symdesk-tools.json`
and `symroom-tools.json`. The local column is the frozen recording: the local
server passes every one of these cases.

Replays executed (all in the workspace feature set, `serde_json` with
`preserve_order`):

| Fixture | Cases | Differ from Go |
|---|---|---|
| `mcp/protocol-process.json` (raw bytes in, stdout, stderr, exit) | 334 | 62 |
| `mcp/tools-process.json` (`tools/list`, line and framed) | 4 | 4 |
| `room/mcp-parity.json`, `room/mcp-mutations.json` (steps) | 341 (5 skipped, see below) | 49 |
| 160 concurrent 300 ms waits (timing probe) | 1 | 3.6 s instead of about 0.3 s |

Five symroom mutation steps are skipped because Go's tool handler itself
panicked (no identity), which a stub cannot reproduce.

### Causes, grouped

A. **Workspace `serde_json` features (symdesk 34 of 62 transcripts and all 4
`tools/list` recordings, symroom 6 of 49 steps).** The root manifest enables
`preserve_order`; CoreKit's own workspace enables `arbitrary_precision` instead.
With `preserve_order` the crate emits `{"protocolVersion":..,"capabilities":..}`,
`{"type":"object","properties":{}}` and `{"type":"text","text":..}` in insertion
order where Go sorts keys (`capabilities`, `protocolVersion`, `serverInfo`;
`properties`, `type`; `text`, `type`). Replaying once with the CoreKit feature
set (temporary edit of the root `serde_json` line, reverted, not committed)
removes exactly these: `tools/list` becomes 4 of 4 byte-identical, symdesk
drops from 62 to 28 diverging transcripts and symroom from 49 to 43.
Changing the workspace feature set is outside this packet and would affect
every crate that relies on ordered `Value` objects.

B. **Genuine, feature independent differences (symdesk 28, symroom 43).**

| Point | Case ids | Go (recorded, = local) | CoreKit |
|---|---|---|---|
| Raw id preservation | symdesk `init-id-07`, `ping-id-07` (`1e3`), `init-id-18`, `ping-id-18` (`1E2`), `init-id-08`, `ping-id-08` (`-0`); symroom `request_ids_preserved` | `"id":1e3`, `"id":1E2`, `"id":-0` verbatim | `1e+3`, `1e+2`, `0`: ids pass through `serde_json::Value` even with `arbitrary_precision` |
| Case-insensitive field names | symdesk `init-jsonrpc-05` (`JSONRPC`), `invalid-28` (`Method`); symroom `uppercase_keys`, `mixed_case_keys`, `call_name_upper_key` | request accepted | `-32600 Invalid Request` / `Unknown tool: ` |
| Invalid UTF-8 and lone surrogates become U+FFFD | symdesk `invalid-32`, `invalid-33`, `invalid-31`, `frame-invalid-utf8-body`; symroom 15 steps in `json_layer_streams` and `mutation_json_layer_streams` (`invalid_utf8_*`, `*lone_*surrogate*`, `high_surrogate_*`) | answered normally, string holds U+FFFD, or `-32600` for a bare `"\ud800"` | `-32700 Parse error: invalid unicode code point ...` or `unexpected end of hex escape ...` |
| Nesting depth limit 10000 (`exceeded max depth`) | symdesk `deep-nesting-5000`, `-9998`, `-9999`, `-10000`, `-20000`; symroom 10 `nesting_*` steps (limit 10000 and 200 levels) | depth up to 10000 accepted, then `invalid character '[' exceeded max depth` | everything above 128 levels (the `serde_json` default) fails with `recursion limit exceeded`, so even 200 levels are rejected |
| Go JSON syntax error text | symdesk `invalid-20` (`{'jsonrpc'...`), `invalid-22` (`"id":01`), `line-nul`, `frame-body-length-chars-not-bytes`; symroom `parse_errors_framed`, `parse_errors_line_mode` | `invalid character '\'' looking for beginning of object key string`, `invalid character '1' after object key:value pair`, `invalid character '\x00' after top-level value`, `... '}' after top-level value` | `invalid character ''' ...`, `invalid number at line 1 column 24`, `'\u0000'` (JSON escaped), `trailing characters at line 1 column 7` (message reconstruction is heuristic) |
| `tools/call` params type error text | symroom `call_name_null`, `call_name_object`, `call_meta_string` | `Unknown tool: ` (null name), `cannot unmarshal object into ... .name of type string`, `cannot unmarshal string into ... ._meta ...` | always `number` / `non-object` wording, null name rejected with `-32602` |
| Transport error text on stderr | symdesk `frame-length-01`, `-13`, `-14`, `frame-header-then-eof-after-blank`, `frame-body-short`, `frame-claim-limit-short-body`, `frame-invalid-utf8-header`; symroom `error_negative_content_length`, `error_truncated_body*`, `error_empty_body_eof`, two `invalid_utf8_*_header` steps | `invalid Content-Length: -1`, `read body: unexpected EOF`, `read body: EOF`, `invalid Content-Length: "\xff"` | `invalid Content-Length: "-1"`, `read body: failed to fill whole buffer`, lossy `"\u{fffd}"` text |
| Ordering and fate of in-flight calls | symroom `parse_error_waits_for_inflight_tool`, `invalid_request_waits_for_inflight_tool`, `fatal_read_error_cancels_inflight_wait` | parse and invalid-request errors are written after the in-flight tool result; a fatal read error still delivers a `context canceled` tool result for the running wait | the error frame is written immediately (before the tool result and even before an unrelated ping), a cancelled wait writes nothing |
| In-flight concurrency | symroom process cases `one-hundred-concurrent-waits-answer-ping-first` (1.4 to 20 s), `six-hundred-concurrent-waits-answer-ping-first` (2.9 to 5 s), unit test `tool_calls_beyond_the_worker_cap_are_queued_without_blocking_the_reader` | one goroutine per call, ping answered while all waits run | at most 16 handlers, and the reader blocks while the cap is full: 160 waits of 300 ms took 3.6 s and the ping came 144th. 600 waits of 3 s would need about 113 s against a 5 s ceiling |

C. **Not executed, identified by reading the crate.** Stdout write failure text
(Go `mcpserver: write response: write /dev/stdout: bad file descriptor`, fixture
`failed-response-write-cancels-a-wait`; the crate prints the OS error text),
and the Unix stdin duplication that lets a write failure interrupt a blocked
read (`stdio_unix.go`; the crate's `serve_stdio` reads `io::stdin()` directly).
SIGPIPE and SIGINT/SIGTERM handling stay in product code either way.

D. **Tool-error `_meta`.** The prior finding listed it. No frozen response
contains `_meta` (the Go handlers return plain errors), so this point neither
helps nor blocks; the crate's `ToolError` metadata is unused by the products.

Parts that agree: `initialize` content, `ping`, notifications, `tools/list`
once key order is sorted, HTML escaping of `<`, `>`, `&` and U+2028/2029,
header size limits (64 KiB, 100 lines), the 1 MiB body limit, recoverable
parse errors, and `Unknown tool:` / `Method not found:` messages.

## What would unblock adoption (upstream gaps)

For `symaira-core-mcp`, in rough order of effort:

1. Parse requests the way `encoding/json` does: case-insensitive member names,
   raw number tokens kept as bytes, U+FFFD replacement for invalid UTF-8 and
   lone surrogates, nesting limit 10000, Go syntax error text.
2. Do not depend on `serde_json::Value` insertion order, or build output maps
   explicitly sorted, so the crate is stable under `preserve_order`.
3. Remove the 16-handler cap or make queued calls not block the reader.
4. Align the transport error text (`%q`, `unexpected EOF`, `EOF`) and the write
   failure text.
5. Re-baseline the oracle on corekit `27d2daf8` (or newer), and add the
   desktop transcripts to the CoreKit contract matrix as an extra oracle.

Partial adoption of a helper was considered and rejected: the only candidate,
`go_json_bytes` (Go HTML escaping), is private, and the local writers already
produce identical bytes (no escaping case differs).

## `symaira-core-mcpcfg` and `symaira-core-update`

* Rust tree: `git grep -n -i "mcpcfg\|mcp_cfg\|selfupdate\|self_update\|self-update\|core-update\|core_update\|claude_desktop_config\|mcpServers\|releases/latest"` over
  `crates/` and `Cargo.toml` finds only unrelated `update` subcommands of
  `mail rules` and `rules`, and the contact-update service. No client-config
  discovery, no config writer for MCP clients, no update check or installer.
* Go tree `36aad3d4`: the only CoreKit Go packages imported were `sqlitekit`,
  `exitcodes`, `vectorkit`, `llmkit`, `configkit`, `mcpserver`, `fsutil`,
  `versionkit`, `secretref`, `logkit`; `git grep` for `mcpcfg`, `selfupdate`,
  `updatekit` finds nothing, and no `update`, `upgrade` or `install` command
  exists besides `mail update` and `rules update`.

Neither crate has a use site, so neither is added.

## Checks

Commands run in the worktree with the external-build prefix; logs are under
`/Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/builds/symaira-desktop/.worktrees/ck-mcp/logs/`
(`chk-*.log`, summary in `chk-summary.txt`). The full workspace gate is run by
the coordinator and is pending.

* `cargo fmt --all --check`
* `cargo clippy -p symdesk-cli -p symroom-cli --all-targets --all-features --locked -- -D warnings`
* `cargo test -p symdesk-cli --locked --test corekit_mcp_compare` and
  `cargo test -p symroom-cli --locked --test corekit_mcp_compare` (new tripwires)
* frozen replays: `mcp_representative`, `mcp_calls`, `mcp_write`, `mcp_protocol`,
  `mcp_aliases`, `mcp_secret_canary`, `mcp_surface`, `mcp_read`,
  `mcp_initialize` (symdesk-cli), `mcp_frame` (symdesk-protocol), `mcp` and
  `mcp_process` (symroom-cli), plus the `mcp` unit tests of both binaries.
  The `mcp_frame` fuzz target was not run (no frame code changed).

Reproduce the evidence: `CK_MCP_REPORT=/tmp/report.json cargo test -p symdesk-cli --test corekit_mcp_compare -- --nocapture`
writes every diverging case with the Go and CoreKit bytes.
