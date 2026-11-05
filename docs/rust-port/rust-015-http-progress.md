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

## Checkpoint (final, candidate `bc1bbfaf`, branch `local/r015-http`; main not merged)

Done:
- Go harness (`cmd/symdesk/http_process_port_test.go`, `http_process_ext_port_test.go`, `http_surface_*_port_test.go`) drives the real Go
  `symdesk serve`; fixture `testdata/port/http/http-surface-process.json` = 84 cases / 1135 steps (routes, paths, auth/rotation/named users,
  files/preconditions/limits/command, shares, fake loopback AI provider, Host/Origin/body/timeouts/shutdown/TLS, serve launch/flag/options cases).
  The Go oracle with the Home Assistant options path is built with `go build -overlay`; Go production source is untouched.
- Rust: `serve --tls-cert/--tls-key/--local-worker/--worker-engine/--ollama-url/--ollama-model` and the `/data/options.json` merge
  (flag > env > options > default; path injectable with the internal `--internal-ha-options=PATH`), `crates/symdesk-cli/src/serve_cli.rs`;
  TLS in `crates/symdesk-protocol/src/tls.rs` (rustls, explicit ring provider, safe default protocol versions; rcgen only as dev-dependency).
  `symdesk serve --help`, `serve -h` and `help serve` are byte-identical to Go (1275 bytes, stdout/stderr/exit compared).
- SEC-002 HTTP canaries: planted tokens and a provider key echoed by a failing fake provider are scanned over every response, log and vault
  snapshot, with a planted-leak negative control. Go reflects the provider key in the error; Rust redacts it (recorded, intentionally stricter).
- Fixed in passing: the HTTP fixture masks the resolver host (`lookup ${1}:`, an earlier `$$1` produced `lookup $1:`); the admin-bodies tree
  snapshots skip Finder `.DS_Store` (host noise, fixture bytes unchanged); journal-date masking of the CLI bodies fixtures.
- Generator digest re-frozen (`provenance.json`); `make port-fixtures-check` is green when it is run to completion.

Evidence (all on clean tree `bc1bbfaf`, receipts in `target/local-gate/receipts.jsonl`):
- Linux arm64: `linux-bc1bbfaf-063657.log` exit 0 (`rust-check rust-lint rust-test port-contract representative-differential http-surface-differential`).
- macOS arm64: `rust-check`, `rust-lint` and the HTTP replays pass; the gate is NOT green. `macos-bc1bbfaf-062051.log` (rust-test) fails
  `symdesk-cli::sources`, `symdesk-cli::signals`, `symdesk-cli::http_surface` (snapshot after external write stays 304),
  `symdesk-index::source_tests::external_source_watch...`, `symdesk-ingest::watcher_contracts`, `symdesk-protocol::snapshot_cache*`;
  `macos-bc1bbfaf-085148.log` fails `sidecar-differential` on the same index watcher test. Cause: `fseventsd` pinned at ~99% CPU on this host
  (4 GB / ~110k files in `/Volumes/1TB_NVMe_SN850X/.fseventsd`), so notify/FSEvents events arrive seconds late or as stale events (Go uses kqueue).
  The same tests fail in the sibling receipts `r014-mcp/.../macos-81525fb0-*.log` and `r-sec004/.../macos-3e8abbea-*.log`; the index watcher test passes
  in isolation when its tmpdir is on the internal volume and the host is quieter. The coordinator's main has bounded nextest retries for these
  replays; the macOS receipt has to be re-taken after integration. `make http-differential` standalone needs `PORT_LEFT/PORT_RIGHT` and runs
  inside `representative-differential` (291 transcripts, green on both hosts).
- `docker`/OrbStack restarts during long runs aborted two Linux attempts (`linux-bc1bbfaf-035804.log` exit 125) and the host ran out of space once;
  those receipts are noise, not evidence.

Declared limits: hyper parser limits above stay (Rust stricter, never more permissive). macOS snapshot freshness after an external write depends on the
FSEvents backend latency (2.5 s settle in both harnesses). Go's fail-open `CanRead/CanWrite` on an unreadable `permissions.json` stays unported
(Rust fail-closed). Go never sets `StateDSN`, so no HTTP route uses PostgreSQL.

Cleanup owed: target dir `/Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/builds/symaira-desktop/.worktrees/r015-http`; scratch `/private/tmp/r015http-tmp`.
