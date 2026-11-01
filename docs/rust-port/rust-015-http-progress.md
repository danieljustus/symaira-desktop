# RUST-015 part HTTP (worker `r015-http`)

Branch `local/r015-http` (base `63f1189b`). Rows: HTTP-001, HTTP-002, HTTP-003, HTTP-005, HTTP-006,
HTTP-007 and the HTTP failure paths of SEC-002. HTTP-004 and SEC-001 are already PASS (local); PG-001
belongs to the sibling `r015-pg` worker and is not touched.

Method: probe the live Go `symdesk serve` and the Rust `symdesk serve` with the same raw-socket request
matrix (disposable HOME/vault, loopback fakes), fix every divergence in the Rust server, then freeze the
Go observations as a Go-recorded process fixture that the Rust replay checks without Go.

## Findings from the first full-matrix probe (all fixed unless listed under limits)

| # | Surface | Go | Rust before | Status |
|---|---|---|---|---|
| 1 | wrong method, unauthenticated | `405` text + `Allow: GET, HEAD` (mux answers before auth) | `401`/`429`, counted as auth failure | fixed (`go_http::serve_mux` runs in front of the whole router) |
| 2 | `//x`, `/./x`, `/a/../x` | `307` to the cleaned path, HTML body for GET | `404` | fixed |
| 3 | malformed `%` escape / control byte in path | `400 Bad Request` text, close | `404` | fixed (`connection_precheck`) |
| 4 | `/%61pi/v1/status` | routes on decoded segments (`200`) | `404` | fixed (canonical path rewrite) |
| 5 | missing / repeated / malformed `Host` | `400` with Go's reason text | served | fixed |
| 6 | `Transfer-Encoding: gzip, chunked` | `501 Unsupported transfer encoding` | decoded | fixed |
| 7 | `Expect: nonsense` | `417` | ignored | fixed |
| 8 | header block > 1 MiB + 4096 | `431` | `200` up to ~1.5 MiB (hyper budget) | fixed (precheck owns the decision; hyper budget raised above it) |
| 9 | `?path=a;b`, `?path=%zz`, repeated `path` | pair dropped / first value wins | `404` / `400` serde error | fixed (`GoQuery` = `URL.Query()`) |
| 10 | `413`, `416` status text | `Request Entity Too Large`, `Requested Range Not Satisfiable` | hyper canonical text | fixed (`ReasonPhrase` override) |
| 11 | `CONNECT host:port` | `404` | `307` | fixed |

## Declared limits (hyper parser layer, not reproducible without replacing the HTTP/1 parser)

- Requests hyper itself rejects (obs-fold continuation lines, `HTTP/1.2` and `HTTP/2.0` versions, `Transfer-Encoding: gzip`
  or `identity`, header names with spaces, URIs above ~64 KiB, fragments in the request target) get hyper's bodiless `400`/`414`
  where Go answers with a text body, or serves the request. Both close the connection; Rust is stricter, never more permissive.
- Connection-level errors written by the adapter (`Host`, `Transfer-Encoding`, `431`, bad escape) use chunked framing plus a
  `Date` header where Go sends a close-delimited body without length; status, reason text, content type and body are identical.
- Header fields above 128 and non-UTF-8 query values stay as documented under HTTP-007.

## Progress log

- Slice 1 (front door): `crates/symdesk-protocol/src/go_http.rs`; existing 291-case `make http-differential` still passes.

## Checkpoint (session end; branch rebased on main 7145e89b, PG-001 merged)

Done:
- Go harness `cmd/symdesk/http_process_port_test.go` (+ `http_surface_port_test.go`): real Go `symdesk serve`, raw socket,
  25 cases / 613 steps in `testdata/port/http/http-surface-process.json`. Symlink-target leak fixed (readlink target goes
  through `maskText`; fixture regenerated, no absolute temp paths). New mask classes `version` and `date`; fixture regenerated.
- `go vet ./cmd/symdesk` clean; `gofmt -l` lists only two pre-existing files (`ask_agent_port_test.go`, `ask_cancel_port_test.go`).
- Rust replay `crates/symdesk-cli/tests/http_surface.rs` + engine `tests/support/http_replay.rs` (dev-deps `regex`, `sha2`);
  the schema already supports fs/tree/restart(rotate)/shutdown/raw steps for the later cases.
- Rust server: `server_state.rs` (job-store dir, permissions manager, one-time legacy-token admin migration into
  `.symdesk/users.json` with the `storage_entries` SQLite `state.db` marker); Go-exact close-delimited connection errors
  written on the socket (`go_http::close_delimited_wire`, `RawAnswered`), replacing the chunked framing listed above as a limit.
- Go `serve` never sets `ServerConfig.StateDSN`, so no route uses PostgreSQL; nothing to wire to `symdesk-storage`.

Last check (macOS, http_surface test): FAILS with 2 divergences left of 613 steps (35 before the fixes):
1. `POST /api/v1/jobs/retry?id=000...0`: Go `{"error":"openat <id>.json: no such file or directory"}`, Rust `job not found`.
2. `GET /api/v1/notebooks/research%00`: Go `notebook not found: open vault file $VAULT/notebooks/research\u0000.md: openat notebooks/research\u0000.md: invalid argument`, Rust `notebook not found`
   (Go also has the `... is not a notebook note (type=...)` and read-error texts; reuse the `symdesk_vault::typed` notebook resolve).

Remaining (not started): fixture cases for HTTP-002 (rotation via restart+rotate, named users), HTTP-003 (file paths,
preconditions, limits, command), HTTP-005 (shares create/list/revoke/expire/access, legacy `token` field cleared on start),
HTTP-006 (fake loopback providers; needs a fake-provider step in the Go harness and replay), HTTP-007 (Host, Origin, body,
timeouts, shutdown, TLS: Rust `serve` has no `--tls-cert/--tls-key`, nor `--local-worker`), SEC-002 failure paths.
Not run after the server_state/raw-error changes: `representative-differential` (291 cases), `http-differential`. Not done:
`port-fixtures-check` digest re-freeze, matrix rows HTTP-001/002/003/005/006/007/SEC-002 and the RUST-015 note, the two-host
local gate, clippy/fmt of the new Rust. Declared: Go's fail-open `CanRead/CanWrite` on an unreadable `permissions.json`
stays unported (Rust keeps fail-closed).
Cleanup owed: target dir `/Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/builds/symaira-desktop/.worktrees/r015-http`; no Docker volumes were created.
