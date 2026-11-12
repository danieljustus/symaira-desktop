# CoreKit `llmkit` -> `symaira-core-llm` (CK-LLM)

Scope: every place the removed Go backend called CoreKit `llmkit` (last Go
commit `36aad3d4`). The Rust successor is the `symaira-core-llm` crate, pinned
with all other CoreKit crates to `592bebb54080d6b37372e47c461cfa3aeca7d2af`.
Rule used: delegate only where the requests, outputs and errors recorded in
the frozen `testdata/port/**` fixtures stay identical; otherwise keep local
code and record the exact difference as an upstream gap.

## Go use sites and their Rust successors

| Go site (`36aad3d4`) | llmkit API | Rust successor | State |
|---|---|---|---|
| `cmd/symdesk/ai_config.go:141` | `Lookup("ollama")`, `NewClient(.. WithBaseURL)` (probe) | `symdesk-cli/src/config_cli.rs:328` | CoreKit |
| `internal/ai/ai.go:341` | `Lookup`, `NewClient`, `Generate` (Ollama `/api/generate`) | `symdesk-core/src/ai.rs` `Provider::stream_with`; all ask, HTTP AI route, admin AI, `prompt_one` go through it | CoreKit (base) |
| `internal/ai/anthropic.go:19` | `Lookup`, `NewClient`, `StreamChat`, `WithStreamFinished` | `symdesk-core/src/ai.rs` (ask) and `symdesk-cli/src/ai_cli.rs` `stream_anthropic_transform` (CLI transform) | CoreKit; transform kept local, see below |
| `internal/ingest/internal/ocr/vlm.go:61` | `Lookup`, `NewClient`, `Generate(WithGenerateImages)` | `symdesk-ingest/src/vlm.rs` | CoreKit (base) |
| `internal/retrieval/internal/engine/embeddings.go:76` | `Lookup`, `NewClient`, `Embed(WithEmbedDimensions)` | `symdesk-protocol/src/retrieval_embedding.rs` `embed_local_ollama` | kept local, see gaps |

## Adopted in this slice

- **CLI `transform`, Ollama/unknown provider** (`symdesk-cli/src/ai_cli.rs`
  `ollama_transform_chunks`). The private `ClientBuilder`/`generate` code in
  `ai_cli.rs` (`stream_ollama_transform`, `OllamaTransformError`,
  `ollama_root`, `ollama_model`, `ollama_endpoint`) is deleted. It now resolves
  `symdesk_core::ai::Provider` (the same CoreKit `generate` boundary the ask
  path uses) and only maps the outcome to Go's transform chunks:
  not-configured text, `Request failed: <unprefixed build error>` and
  `Request failed: ollama: <stream error>`, with `go_transport_error` for the
  refused-connection wording as before. Effect on the wire: the request body
  now carries Go's `encoding/json` HTML escaping (`\u003c \u003e \u0026`
  `\u2028 \u2029`) and the NDJSON decoder tolerates chunks whose `model` or
  `done` is omitted or null, which closes the "GenerateResponse requires
  fields Go's decoder tolerates" gap noted in
  `corekit-adoption.md` for the transform path. No frozen fixture records the
  CLI Ollama transform transport (only Hermes: `transform-hermes-process.json`),
  so the pinned tests are the unit tests in `ai_cli::tests` (request line, body
  escaping, omitted fields, callback failure stops the stream, error prefixes).

## Kept local (with reasons)

- **CLI `transform`, Anthropic** (`symdesk-cli/src/ai_cli.rs:556`
  `stream_anthropic_transform`). It uses CoreKit already (`stream_chat_cancellable`
  at `ai_cli.rs:592`). `symdesk_core::ai::Provider` streams through the blocking
  `stream_chat` (`symdesk-core/src/ai.rs:150`) and only observes a failed
  `max_tokens` marker write after the stream returns (`ai.rs:165-172`). The
  transform contract (pinned by
  `max_token_marker_write_error_cancels_a_stalled_provider_stream`) cancels the
  in-flight response when that write fails, and the async path keeps reqwest's
  environment proxy support, which the cancellable ureq agent cannot have.
  Delegating would regress both. No fixture covers it.
- **Retrieval embeddings** (`symdesk-protocol/src/retrieval_embedding.rs`):
  see upstream gaps; verified against CoreKit with a throwaway loopback probe.
- **Agentic loop `agent_turn` / `symdesk-service/src/agent.rs`, HyDE query
  expansion and rerank** (`symdesk-core/src/agent_turn.rs`,
  `symdesk-protocol/src/retrieval_query_expansion.rs`): Go called `net/http`
  directly (`internal/ai/agent_turn.go:9`, `internal/retrieval/internal/engine/rerank.go:214`),
  never `llmkit`; there is no CoreKit counterpart for tool-call turns, so these
  are out of CK-LLM scope.
- `symdesk-ingest/src/vlm.rs`, `symdesk-core/src/ai.rs`,
  `symdesk-cli/src/config_cli.rs`: already CoreKit; their Go wire adapters
  (`go_agent_config`) are the documented thin adapter, unchanged here.

## Upstream gaps (`symaira-core-llm` @ 592bebb)

Embeddings (`Client::embed`) cannot replace `embed_local_ollama` byte for byte.
Probe (loopback server, `Client::embed("m", ["a"], Some(3))`) results:

1. `{"data":[{"embedding":null}]}`: CoreKit
   `llmkit: provider_error: decode embeddings response: invalid type: null, expected a sequence`
   (`embeddings.rs:313`, field decoded as `Vec<f32>`); Go and the local code
   return an empty vector (fixture case `null-vector-is-empty-vector` in
   `testdata/port/retrieval/embedding-http.json`; local
   `retrieval_embedding.rs:368`).
2. `{"data":null}` and `{}`: CoreKit fails with `invalid type: null ...` /
   ``missing field `data` `` (`embeddings.rs:277,283`); the local decoder returns
   zero vectors and then reports Go's count error
   `expected N embeddings, got 0` (`retrieval_embedding.rs:323`).
3. Refused connection: CoreKit `llmkit: transport_error: io: Connection refused`
   (`client.rs:374`, ureq text); the frozen CLI/MCP transcripts record
   `llmkit: transport_error: Post "http://127.0.0.1:$PORT/v1/embeddings": dial tcp
   127.0.0.1:$PORT: connect: connection refused` (six retry-log lines in
   `cli-bodies-process.json`, `cli-errors-process.json`, `index-retrieval-process.json`,
   `calls-process.json`, `http-surface-process.json`), produced locally by
   `go_transport_message` (`retrieval_embedding.rs:196`).
4. Timeout: ureq `transport_error: timeout: global` vs Go
   `transport_error: context deadline exceeded` (local `retrieval_embedding.rs:57`).
5. Default blocking agent sends `user-agent: ureq/3.4.1` and `accept-encoding: gzip`;
   Go sent `Go-http-client/1.1` (CoreKit only sets it on the reqwest path,
   `client.rs:204,250`). `embed` is blocking-only; the cancellable async variant
   uses reqwest and cannot take the Go wire adapters.

Closing 1 to 4 needs either CoreKit-side Go-tolerant decoding and Go-shaped
transport error text, or a response-body and error rewriting middleware around
`embed` that would re-implement the private transport. Not done.

## Checks

Run with `CARGO_TARGET_DIR=/Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/builds/symaira-desktop/.worktrees/ck-llm/target`,
`CARGO_BUILD_JOBS=3`, `TMPDIR=/private/tmp/symdesk-ck-llm` via `dev-external`:

```sh
cargo fmt --all --check
cargo clippy -p symdesk-cli -p symdesk-core --all-targets --all-features --locked -- -D warnings
cargo test -p symdesk-cli --bin symdesk ai_cli::tests --locked
cargo test -p symdesk-cli --locked --test transform_hermes --test ask_offline --test ask_hermes \
  --test secret_canary --test mcp_secret_canary --test config_vault_selection \
  --test mcp_ask_provider --test ask_cancel --test index_reembed_http
cargo test -p symdesk-core --locked --test ai_providers
cargo test -p symdesk-service --locked --test ai_compose
cargo test -p symdesk-ingest --locked --test vlm_contracts --test ocr_contracts
cargo test -p symdesk-protocol --locked --test retrieval_embedding
```

The full workspace gate is pending (coordinator).
