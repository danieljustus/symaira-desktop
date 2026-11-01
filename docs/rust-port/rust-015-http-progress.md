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
