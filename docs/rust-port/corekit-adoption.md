# CoreKit foundation adoption

## Version handshake slice

The staged Rust `symdesk` and `symroom` CLIs consume `symaira-core-version`
through `symdesk-core`, pinned to CoreKit commit
`d382b8615ce879bac6f23c7250e13667934e8f93` with `Cargo.lock` resolution.
The crate remains private (`0.0.0`, `publish = false` upstream); this is a Git
adoption, not a crates.io or product release.

The local `VersionDocument` duplicate is removed. CoreKit owns the payload
fields and plain-text formatting. Desktop retains its thin newline/JSON
serialization adapter and existing CLI parser behavior, including default
versions, inherited flags, extra arguments and error exit codes. This slice
does not switch to CoreKit's separate Go-escaping JSON writer or alter the
existing public rendering functions' signatures.

Verification commands:

```sh
cargo fmt --all --check
cargo clippy --workspace --all-targets --all-features --locked -- -D warnings
cargo nextest run --workspace --all-features --locked
cargo test --workspace --doc --all-features --locked
make rust-version-contract
cargo audit
cargo deny check
```

The initial local macOS run passed all 52 workspace tests and all 17 selected
Go/Rust version differential cases, using both built release binaries.
Native Linux/Windows evidence must come from CI on the adoption revision;
local success does not establish those platforms or product-release adoption.

No Go implementation, fallback, Swift bridge, HTTP value gate or migration
work-item completion status is changed. Subsequent foundation families need
separate behavior review; CoreKit exit categories must not replace the CLI's
existing numeric exit behavior by assumption.

## Exit code slice

The audit covered every `ExitCode` return path in `crates/symdesk-cli/src/main.rs`
and `crates/symroom-cli/src/main.rs`. `symdesk` uses only process codes 0 and 1:
success paths map to `symaira_core_exit::ExitCode::Ok` (0), and all existing
error paths map to `ExitCode::Generic` (1). The shared taxonomy is therefore
used only where its numeric value is identical; output, parser, and error
behavior remain unchanged.

`symroom` retains its existing literal process code 2 for missing, unknown, or
invalid subcommands (`crates/symroom-cli/src/main.rs:26`, `:37`, and `:56`).
That code is `symaira_core_exit::ExitCode::NoInput`, but this slice does not
reinterpret the established Rust CLI contract, so the call sites remain
literal 2. Its success and generic I/O/serialization failures adopt CoreKit
`Ok` (0) and `Generic` (1). CLI integration tests pin representative process
statuses for both binaries, and a focused unit test pins the complete CoreKit
0–10 taxonomy.

## Config slice

**Audit conclusion: adoption deferred.** `symdesk-core` has a product-specific
`Config` schema and validation (`crates/symdesk-core/src/config.rs:9-80,
151-250`), but its generic-looking helpers are not behavior-compatible with
CoreKit's `symaira-core-config` at the pinned revision. No Cargo dependency or
loading call-site change was made: routing through CoreKit would change the
observable contract frozen by `crates/symdesk-core/tests/config_contracts.rs`.

The exact CoreKit rows reviewed were CFG-001 through CFG-007 in
`symaira-corekit/docs/rust-port/contract-matrix.json:351-454`. The comparison is:

- **Default paths (CFG-001):** there is a partial duplicate. Desktop exposes
  data/config/cache home and directory helpers plus a global path
  (`config.rs:311-349`), while CoreKit exposes only the config-file path and
  legacy option (`symaira-core-config/src/lib.rs:90-113, 193-201`). Desktop's
  helpers return `String`, accept portable Windows-looking absolute paths on
  every host (`config.rs:392-394`), and fall back to `./...` when HOME is
  absent (`config.rs:385-390, 396-405`). CoreKit uses host-native
  `PathBuf::is_absolute` and platform-selected HOME/USERPROFILE, and its
  `Loader` returns `cannot determine home directory` when no home exists
  (`symaira-core-config/src/lib.rs:107-132, 188-191`). These paths and failure
  behavior are not identical.
- **Precedence (CFG-003):** the conceptual order overlaps, but the loaders do
  not. Desktop `load` consumes one caller-supplied optional TOML string and
  then its explicit environment snapshot (`config.rs:284-300`); it does not
  read a global file, project `.<name>.toml`, or cache. CoreKit always checks
  the global file, then the project file, then the process environment
  (`symaira-core-config/src/lib.rs:192-211`). CoreKit's `Loader` also caches
  its first result and provides reload/reset operations (CFG-007;
  `symaira-core-config/src/lib.rs:135-186`), whereas Desktop has no such
  state. Therefore the nominal defaults < global < project < env order cannot
  be adopted without adding files and changing public behavior.
- **Environment handling (CFG-004/005):** Desktop applies a fixed, product
  allowlist, ignores empty values, validates numeric ranges before assignment,
  and applies all four documented #854 overrides alongside the older fields
  (`config.rs:88-164, 376-391`). CoreKit derives `{PREFIX}_{FIELD}` and nested
  names from serialized schema fields, parses bool/int/float/array values, and
  returns typed parse errors (`symaira-core-config/src/lib.rs:253-293,
  382-496`). This is a product adapter seam, not an identical generic
  implementation.
- **TOML merge and errors (CFG-002/006):** Desktop deserializes the complete
  supplied document directly and wraps failures as
  `failed to decode config file: ...` (`config.rs:289-296`). CoreKit reads
  filesystem paths, skips only NotFound, checks map/type compatibility,
  merges only non-zero overlay values, and prefixes errors with the path and
  operation (`symaira-core-config/src/lib.rs:215-250`). Those bytes and merge
  semantics differ, including the fact that CoreKit can produce global/project
  read/parse/apply errors that Desktop cannot produce.

The existing generated config fixture and tests explicitly pin Desktop's
single-input/allowlist/path behavior (`crates/symdesk-core/tests/config_contracts.rs:104-170`),
so this is evidence for deferral rather than a missing implementation. A
future adoption would require a CoreKit-compatible adapter or a separately
reviewed CoreKit change, followed by regenerated Go differential evidence; it
must not silently replace these semantics.

## Log slice

Log adoption is not applicable yet. The desktop Rust workspace has no local
logging framework duplicate: the source inventory finds only diagnostic
`eprintln!` calls in `crates/symdesk-protocol/src/lib.rs:138`,
`crates/symdesk-index/src/bin/sidecar-rust-helper.rs:52`, and a test diagnostic
in `crates/symdesk-index/src/contract_tests.rs:591`. There is no `tracing`,
`log`, `env_logger`, `fern`, or `slog` implementation to remove or route
through CoreKit's `symaira-core-log`; the CoreKit LOG-001–LOG-004 family is
therefore outside this adoption slice.

## FS slice

**Audit conclusion: adoption deferred.** The desktop Rust workspace already
uses `cap-std` for capability-scoped reads, but neither usage is a behavior-
identical duplicate of CoreKit's `symaira-core-fs`. No Cargo dependency or
call-site change was made.

The reviewed CoreKit rows are FS-001 through FS-007 in CoreKit's
`docs/rust-port/contract-matrix.json`. CoreKit's `validate_path` is a lexical
relative-path validator and its secure operations provide atomic writes, safe
removal, directory creation, locking, and Unix `openat(O_NOFOLLOW)` confinement
(`rust/symaira-core-fs/src/lib.rs:52-78, 180-250, 257-373, 392-503, 813-864`
at the pinned CoreKit revision). The desktop call sites do not expose
those operations with the same observable contract:

- `symdesk-index` uses `cap_std::fs::Dir` to keep the vault root open while
  indexing and pruning, then reads metadata and bytes from the same opened
  handle (`crates/symdesk-index/src/lib.rs:352-424, 499-533`). Its generic-
  looking `storage_path` is also product-specific: it preserves Go's separate
  I/O and database-key path spellings, rejects non-UTF-8 keys, and delegates
  symlink confinement to the vault's canonicalization policy
  (`crates/symdesk-index/src/lib.rs:704-724`). Replacing this with CoreKit
  helpers would change index keys, error variants, or the no-read cache path.
  Directory creation for the sidecar additionally applies the product's
  `0700` policy to newly missing parents (`crates/symdesk-index/src/lib.rs:766-791`),
  not CoreKit's `safe_mkdir_all` API at an equivalent call site.
- `symdesk-protocol` opens the current vault root as a capability and uses
  handle-relative reads for HTTP file/range responses and snapshots
  (`crates/symdesk-protocol/src/lib.rs:398-437, 524-552, 578-606`). Its
  `confined_path` deliberately rejects backslashes, dot segments, empty
  segments, and `.symdesk` before the capability open
  (`crates/symdesk-protocol/src/lib.rs:639-667`). This is an HTTP wire-contract
  validator, not CoreKit's `validate_path`; its errors are translated to the
  fixed `400`/`404` responses at `:411-427`. The snapshot watcher also needs
  ordinary path-based create/write/rename/delete events in tests
  (`crates/symdesk-protocol/src/snapshot_cache.rs:327-352`), which are not
  generic CoreKit safe-tree operations.
- `symdesk-vault/src/paths.rs` is a separate secure-path resolver: it preserves
  the Go `filepath.Join` absolute-input behavior, canonicalizes existing and
  missing targets, and returns distinct traversal, vault, parent, path, and
  symlink-escape errors (`crates/symdesk-vault/src/paths.rs:10-48, 59-75`).
  CoreKit's lexical `validate_path` rejects absolute paths and returns
  `FsError::InvalidPath`, so adopting it would change both accepted paths and
  error mapping. The vault walker likewise intentionally reports symlink
  entries and sorts directory results (`crates/symdesk-vault/src/walk.rs:38-41,
  93-129`), rather than using CoreKit's mutation primitives.

There is therefore no genuine behavior-identical FS duplicate to remove in
this slice. Keep `cap-std` and the local path code until a future adoption
introduces an adapter whose path spelling, errors, permissions, and symlink
handling are proven byte/behavior-equivalent by the FS differential harness.

## SecretRef slice

SecretRef is not applicable to the desktop Rust workspace. An exact search of
all Rust sources under `crates/` found no `symvault://`, `secretref`, or
`SecretRef` call sites, so `symaira-core-secretref` was intentionally not
added. The SEC-001 CoreKit contract row remains outside this slice; the
references currently present in Go/Swift/docs are not Rust dependency call
sites to adopt.

## MCP slice

**Audit conclusion: adoption deferred.** CoreKit's `symaira-core-mcp` is a
substantial generic implementation, but it is not an exact wire-compatible
replacement for Desktop's MCP path. No Cargo dependency or call-site change
was made. A thin adapter could retain the product tools, but it could not
safely delegate framing, request validation, response serialization, and error
mapping without changing observable bytes or ordering.

The Desktop inventory is in `crates/symdesk-cli/src/mcp.rs`:

- `serve`/`serve_io` establish the stdio boundary, resolve `SYMDESK_VAULT`,
  keep stdout protocol-only, and send diagnostics only through the returned
  `io::Result` for `main.rs` to print to stderr (`:85-106`,
  `crates/symdesk-cli/src/main.rs:97-101`). The loop dispatches `tools/call`
  on worker threads but dispatches all other requests inline and joins workers
  in submission order (`:111-191`). This ordering and the transport-failure
  fallback responses are part of the current behavior.
- `initialize`, `ping`, `tools/list`, and unknown methods have fixed result
  shapes, protocol version, `symdesk` server name, and method error strings
  (`:194-235`). Tool order and schemas are literal product data, including
  `annotations.readOnlyHint` (`:376-397`), and tool results are always the
  product's text `content` plus `isError` envelope (`:420-443`). Product
  parsing and sidecar behavior remain in `call_tool` (`:291-355`).
- Requests accept newline JSON or `Content-Length` framing, enforce a 1 MiB
  line/body limit, recognize only an exact `Content-Length:` prefix, and turn
  malformed JSON into `-32700` with a `Parse error: ...` message
  (`:492-593`). A notification is any object without an `id` and is silently
  ignored, including unknown methods and `notifications/cancelled`
  (`:203-206`, `:583-590`). The existing tests pin line/framed output,
  notification silence, malformed input, oversized lines, and truncated
  frames (`:630-744`). The implementation has no cancellation token or
  request-metadata path; a cancellation notification is not connected to an
  in-flight call.

The CoreKit API and implementation were reviewed at the pinned CoreKit source
`rust/symaira-core-mcp/src/lib.rs`:

- CoreKit's public seam is `Server::new`, `register_tool`/`register_typed`,
  `serve_io`/`serve_io_cancellable`, `CancellationToken`, `ToolOutput`, and
  structured `ToolError` (`:25-218`, `:425-604`). It deliberately adds typed
  schema normalization/strict argument decoding (`:473-541`), request `_meta`
  propagation (`:842-871`), a 16-call limiter (`:342-395`), and cooperative
  cancellation (`:561-604`). Those are not present in Desktop's product
  adapter and cannot be introduced under an exactness-only adoption.
- CoreKit distinguishes JSON-RPC invalid requests (`-32600`) from parse errors
  and rejects missing/non-`2.0` JSON-RPC, invalid IDs, and invalid `params`
  during framing (`parse_json_request`/`valid_raw_id`). Desktop instead parses
  any JSON object, defaults a missing method to `""`, treats `[]` as a parse
  error with `-32700`, and does not validate `jsonrpc`, ID type, or params shape
  at the framing boundary (`crates/symdesk-cli/src/mcp.rs:557-592`). This is a
  direct mismatch in the `invalid-request` fixture and in malformed request
  edge cases.
- CoreKit preserves raw JSON-RPC IDs with `Box<RawValue>` and emits Go-style
  JSON escaping through `go_json_bytes` (`:923-1014`, `:1140-1185`), while
  Desktop parses IDs into `serde_json::Value` and serializes them again
  (`crates/symdesk-cli/src/mcp.rs:34-49`, `:557-592`, `:467-490`). ID lexical
  spelling and HTML-sensitive string bytes therefore cannot be delegated while
  promising byte equality.
- CoreKit writes tool errors as successful MCP results with structured
  `_meta["symaira.dev/tool_error"]` data for `ToolError` (`:1070-1132`), and
  supports `structuredContent`/`_meta` on successful results (`:105-140`).
  Desktop's `RpcError` has only `code` and `message` (`:41-55`), while product
  tool failures are flattened to text with `isError: true` and no `_meta` or
  `structuredContent` (`:279-287`, `:420-443`). This directly conflicts with
  contract rows CON-007 and MCP-007, whose expected tool-error metadata and
  omission rules are byte comparisons.
- CoreKit's transport separates protocol errors from terminal transport errors,
  supports bounded headers (64 KiB/100 lines), and can return `Cancelled`
  without joining a blocked reader (`:543-737`, `:1128-1210`). Desktop treats
  malformed/truncated framed input as a fatal `io::Error` after any pending
  workers are joined (`crates/symdesk-cli/src/mcp.rs:504-540`, `:126-147`),
  has no cancellable reader, and uses a different header policy. The two
  implementations thus differ on stdio edge cases even where ordinary frames
  look alike.

The CoreKit contract matrix rows reviewed were CON-003, CON-006, CON-007, and
MCP-001 through MCP-012 in CoreKit's
`docs/rust-port/contract-matrix.json`. Desktop's frozen oracle is
`testdata/port/mcp/representative.json`; the differential target is
`Makefile:193-204` (`mcp-fixtures-check` and `mcp-differential`), and the
existing Rust MCP unit tests are in `crates/symdesk-cli/src/mcp.rs:630-744`.
Those fixtures cover initialization/capabilities/serverInfo, tool order and
schemas, calls and text envelopes, notifications, IDs, malformed/oversized
input, framed transport, EOF, cancellation notification silence, and stdout
purity. The matrix additionally requires structured tool results and error
metadata, which the Desktop slice intentionally does not expose today.

**Future adapter seam:** retain `serve_io`/`read_request`/`write_response` and
all product dispatch/parser functions as the compatibility boundary. If a
future CoreKit revision exposes a compatibility mode for Desktop's permissive
request parser, `Value` ID serialization, header limits, terminal-error
behavior, and no-`_meta` tool envelope, add focused Go/Rust fixture cases for
each delegated path before adoption. Do not use `Server::serve_io` directly or
add the git dependency until every CON/MCP byte/process comparison passes and
stdout remains protocol-only. No work-item or contract-matrix status was
changed.

## SQLite connection-opening slice

The desktop Rust workspace adopts `open_with_existing_parent` from
`symaira-core-sqlite` for connection-opening, pinned to immutable CoreKit source
commit `62edd9903983d9369373565cc1e50da3fef43176` (crate version `0.0.0`,
`publish = false` upstream). The verified implementation commit in
`symaira-desktop` is `524de84399274c0fb9ea0a9873c3e2e9f78ad3a9`.

This is a narrow diagnostic Git adoption, not a crates.io publication, not full
RUST-006 or VALUE-001 completion, and not a product cutover.

### Boundary and retained behavior

Only `open_with_existing_parent` is adopted for opening SQLite database
connections. Desktop explicitly retains:
- Its local `0700` permission and ancestor directory creation policy for newly
  missing parent paths before opening.
- Schema migrations, table creation, and backfill routines.
- Transaction semantics and query execution models.
- Original `rusqlite` diagnostic errors and status mappings.

Unrelated source implementations, deferrals, contract register statuses, and
fixtures remain unchanged.

### Verification and CI evidence

Hermes independently verified the implementation tree subsequently committed as
`524de84399274c0fb9ea0a9873c3e2e9f78ad3a9` using Go 1.26.6 / Rust 1.98.0:
- `cargo fmt --all --check`
- `cargo clippy --workspace --all-targets --all-features --locked -- -D warnings`
- Workspace tests and `make representative-differential`

Full native GitHub Actions CI run `34902569823` at the exact implementation SHA
succeeded across all three target platforms:
- Windows: [job/104171714110](https://github.com/danieljustus/symaira-desktop/actions/runs/34902569823/job/104171714110)
- Linux: [job/104171714197](https://github.com/danieljustus/symaira-desktop/actions/runs/34902569823/job/104171714197)
- macOS: [job/104171714248](https://github.com/danieljustus/symaira-desktop/actions/runs/34902569823/job/104171714248)

Test coverage details:
- Three new portable `sidecar_open` tests run and pass across all three
  operating systems (Windows, Linux, macOS).
- An additional symlink-policy test is Unix-only; no Windows symlink proof is
  claimed from it.
- An independent read-only review found no blockers.

Public logs are accessible via the GitHub Actions job URLs above. Central local
evidence artifacts are recorded in `consumers-night-desktop-native-<jobid>.log`
and `consumers-night-desktop-sqlite-patch-20260914T220549.373289Z/run.json`
(referenced without personal absolute paths).

## Anthropic transform transport slice

The staged Rust `symdesk transform` command delegates only the Anthropic
streaming transport to `symaira-core-llm`, pinned to CoreKit revision
`04d1411adb57aa602b992509121011aa7666ff1a` (`0.0.0`, private upstream). The
consumer keeps its Go-compatible intent prompt, language rule, configured
model/max-token values, five-minute timeout, secret resolution order, chunk
output format, and visible credential/configuration errors. Provider base URL
overrides continue to use the existing `SYMDESK_ANTHROPIC_URL` setting. CoreKit
owns the Anthropic request and SSE parsing; the CLI writes each delta and
flushes it as it arrives. API keys are redacted from provider error bodies.

An empty configured model falls back to Desktop's established
`claude-sonnet-5`, rather than CoreKit's different descriptor default. Client
construction errors retain the unwrapped prefix, while errors after streaming
starts retain Go's `anthropic:` prefix. A failed output write cancels the
cancellable CoreKit stream, including the otherwise-infallible max-token
finish callback.

This is CLI `transform` coverage only. It does not add Anthropic support to the
HTTP AI route or change the Ollama/Hermes paths. The Go implementation remains
the production oracle. The CoreKit pin raises the shared Tokio pin to `1.53.1`
so Cargo resolves one Tokio version for both Desktop and CoreKit.

Focused local checks:

```sh
cargo fmt --all --check
cargo test -p symdesk-cli --bin symdesk ai_cli::tests --locked
umask 0022 && cargo test -p symdesk-core -p symdesk-cli -p symdesk-protocol --locked
cargo clippy -p symdesk-core -p symdesk-cli -p symdesk-protocol --all-targets --locked -- -D warnings
```

At the adoption worktree, the focused CoreKit transport tests, the selected Go
`internal/ai` transform tests, and the full Rust `symdesk-core`, `symdesk-cli`,
and `symdesk-protocol` test suites passed on Linux. A separate local process
smoke used an isolated HOME/XDG tree and a chunked loopback Anthropic provider;
it checked prompts, model, endpoint path, JSON escaping, plain `{chunk}` lines,
the truncation marker, empty stderr, exit status, and delivery of the first
chunk before the provider released the rest of its response. This local smoke
is not native cross-platform or release evidence. The protocol test suite in
this environment requires umask `0022`; the session default `0077` masks the
existing upload file mode expectation from `0640` to `0600`.

## Ollama transform transport slice

The Rust `symdesk transform` command now delegates Ollama's native generation
transport to the same pinned `symaira-core-llm` revision. CoreKit owns the
`/api/generate` request and NDJSON decoding; the consumer preserves the Go
transform prompt and chunk output, uses the configured Ollama URL (including
the existing `SYMDESK_OLLAMA_URL` config override), strips any endpoint path
before native requests, and keeps the five-minute timeout. A non-empty
`SYMDESK_OLLAMA_MODEL` wins; otherwise transform uses Go's `llama3.2` default,
independently of the configured Anthropic model. Empty responses are ignored,
while non-empty responses are emitted as received even when the response marks
`done`.

Provider selection retains the Go fallback: empty, `ollama`, and unknown
provider values use Ollama; `anthropic` uses the preceding adapter; Hermes
remains explicitly unsupported by this Rust transform path. Client-construction
errors remain unprefixed, and request/stream errors keep Go's `ollama:` prefix.
A failed stdout write is passed back through CoreKit's generator callback to
stop generation. The pinned native `generate` API is synchronous and exposes
no cancellation token, so this early stop cannot guarantee that an in-flight
HTTP operation is interrupted; the five-minute timeout remains the bound.

Focused transport tests cover the native endpoint and request body, URL-root
stripping, default and explicit model selection, empty and final chunks,
callback failure, provider error rendering, and Go-compatible chunk bytes.
An isolated HOME/XDG CLI-process smoke also passed three chunked HTTP/1.1
cases: configured, empty and unknown provider selection, environment URL/model
priority, native root path, trimmed input and language prompt, JSON/plain chunk
bytes, delivery before EOF, and continued records after `done`. Each process
exited successfully with empty stderr. This is Linux evidence only.
The transport test uses complete valid Ollama response
objects. CoreKit's `GenerateResponse` requires its response fields during
deserialization, while Go's JSON decoder can leave omitted fields at zero
values; incomplete or null-field provider lines may therefore fail in Rust
where Go would continue. This slice does not claim complete parser parity for
malformed provider streams.
