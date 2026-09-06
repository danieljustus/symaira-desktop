# Rust port architecture

> **Status:** accepted; staged implementation active
> **Produces:** an optional Rust implementation beside the Go oracle

## 1. Why this shape

The current Go tree combines a mature Markdown workspace, multiple absorbed
products, a large Cobra surface, a hand-written second CLI, MCP, authenticated
HTTP, SQLite/FTS5, PostgreSQL, OCR, mail, PDF, AI, and release packaging.
Mirroring each Go package as a Rust crate would preserve accidental coupling and
create dozens of crates. The Rust design follows independently testable product
capabilities instead.

The highest production-code areas are `internal/ingest` (12,738 lines),
`internal/retrieval` (9,049), `cmd/symdesk` (8,107), `internal/service` (8,046),
`internal/draw` (6,412), `internal/contacts` (5,361), `internal/room` (3,822),
`internal/selfhost` (3,438), and `internal/sidecar` (2,881). These seams and the
external contracts determine the slice order.

## 2. Must support

| Capability | Why |
|---|---|
| Existing contract-v1–v6 Markdown vaults, including v5 aliases/bases, v6 datasets/hybrid metadata, attachments, notebooks, views, unknown fields, and conflict copies | `VAULT.md` v6 is current; all earlier additive shapes remain supported and Markdown is the source of truth |
| Existing SQLite schemas/migrations and PostgreSQL self-host mode | Operators must not lose or manually transform derived/server state |
| All `symdesk` and `symroom` argv, flags, aliases, outputs, errors, signals, and exit codes | Apps, scripts, and users depend on them |
| 57-tool SymDesk and 8-tool SymRoom MCP surfaces | Agent integration is a core product surface |
| Authenticated HTTP API, workers, shares, streaming AI, limits, and shutdown | Native and self-hosted clients depend on it |
| OCR, mail, Paperless, PDF operations, archive/PDF-A behavior, draw/diagram output, and external-engine fallbacks | Document lifecycle is the product, not an optional edge |
| macOS/Linux/Windows arm64+amd64 binaries and current archive names | Distribution compatibility |
| Native Swift consumers through their real transports: macOS CLI/events, iOS authenticated server API, and iOS security-scoped Files/iCloud access without a CLI | UI migration is out of scope, but every consumer transport must survive backend cutover |

## 3. Out of scope

| Capability | Reason |
|---|---|
| Rewriting SwiftUI apps, `meet/`, or `room/client/` | Swift is the appropriate native surface |
| A new vault, dataset, or database schema | A language port and data-model migration cannot be debugged safely together |
| New CLI commands, MCP tools, HTTP endpoints, or AI features | Prevents feature drift from being disguised as parity work |
| Replacing external Typst, Tesseract, Poppler, Ollama, or Symaira tool protocols | Existing runtime integration contracts remain authoritative |
| A cross-repository Cargo workspace or shared unpublished Rust internals | Violates repository independence and standalone-first |
| Treating SQLite as the Markdown source of truth | Violates the core product invariant |
| Rustifying generated or native UI code for language-count aesthetics | Adds risk without product value |

## 4. Delivery and rollback

Rust lands in this repository beside Go. Production stays Go until cutover.

- Oracle binaries: `symdesk-go` and `symroom-go`, built from the pinned commit.
- Candidate binaries: `target/release/symdesk` and `target/release/symroom`.
- Before cutover, release assets remain the current Go binaries.
- The first cutover prerelease uses Rust names and carries `symdesk-go` and `symroom-go` fallbacks in the same archive.
- The stable cutover keeps both fallbacks for one stable release.
- Go removal is a separate reviewed change after one stable Rust release operates without unexplained parity defects.
- Rollback must prove that Go can reopen and safely mutate a copy last written by Rust, including each SQLite store and room journal.

The Homebrew formula, Docker image, Home Assistant add-on, signed CLI assets,
macOS app, and iOS app remain on their existing delivery paths until their
consumer and artifact gates pass.

## 5. Crate boundaries

Create a crate only when its first slice starts. Empty future crates are not
architecture.

| Crate | Responsibility | Initial Go seams |
|---|---|---|
| `symdesk-core` | Domain types, errors, output contracts, config models, search-query grammar, text normalization, hashes, SimHash | `internal/config`, `searchquery`, `documentformat`, `textnorm`, `simhash`, pure service types |
| `symdesk-vault` | Markdown/frontmatter parsing, safe paths, atomic files, attachments, notebooks, datasets, views, history/trash/conflicts, retention metadata | `internal/vault`, `storage`, `notebook`, `dataset`, `dbviews`, `history`, `retention` |
| `symdesk-index` | Sidecar, FTS5, retrieval/chunking/ranking/embeddings, contacts and their migrations | `internal/sidecar`, `retrieval`, `contacts` |
| `symdesk-ingest` | Queue/store, extraction, OCR adapters, mail, Paperless/Notion import, classification, archive operations | `internal/ingest`, `mail`, `paperless`, `archive`, `templatepath` |
| `symdesk-render` | Draw IR/parser/layout/emit, PDF operations, Typst adapter, HTML/CSV/PDF export | `internal/draw`, `pdf`, `export` |
| `symdesk-service` | Use cases, composition, AI, recipes, health, permissions, journal, watcher coordination | `internal/service`, `compose`, `ai`, `recipes`, `health`, `permissions`, `journal`, `watcher` |
| `symdesk-protocol` | Canonical MCP registry/adapters and authenticated HTTP/worker/share transports | `internal/tools`, `mcp`, `selfhost` |
| `symdesk-cli` | Thin `symdesk` command tree, output rendering, process exit mapping | `cmd/symdesk` |
| `symroom-core` | Ed25519 identities, signed JSONL journal, membership, approvals, runs, artifacts, index | `internal/room` except MCP |
| `symroom-cli` | Hand-written-compatible `symroom` parser, MCP adapter, binary composition | `cmd/symroom`, `internal/room/mcp` |

### Dependency direction

```text
symdesk-cli      -> core + vault + index + ingest + render + service + protocol
symdesk-protocol -> core + service + narrow capability traits
symdesk-service  -> core + vault + index + ingest + render
symdesk-ingest   -> core + vault + index adapter traits
symdesk-render   -> core + vault adapter traits
symdesk-index    -> core + vault read models
symdesk-vault    -> core
symroom-cli      -> symroom-core + protocol adapter
symroom-core     -> symdesk-core only where wire/error primitives are genuinely shared
symdesk-core     -> no adapter crates
```

The `RUST-002` version-only `symroom-cli` temporarily uses the shared version
renderer in `symdesk-core`; it does not create an empty `symroom-core` crate.
`symroom-core` starts with the real signed-journal slice in `RUST-016`, after
the early value gate. The dependency direction above applies from that point.

Core crates do not depend on clap, Tokio, HTTP, MCP, SQLite, PostgreSQL, OS
keyrings, external processes, or Swift. Cycles are resolved with narrow traits
owned by the consuming domain, not a generic service-locator crate.

## 6. Framework and dependency choices

| Area | Candidates | Decision | Contract constraint |
|---|---|---|---|
| CLI | `clap` vs hand parser | Use `clap` for `symdesk`; keep a compatibility-focused parser option for `symroom` | Cobra and current `flag.FlagSet` behavior are black-box fixtures, not assumed equivalents |
| MCP | official `rmcp` vs `rust-mcp-sdk` vs minimal transport | Spike official `rmcp` behind an adapter first | Raw framing, ordering, annotations, cancellation, limits, and zero stdout pollution decide |
| HTTP | `axum`/`tower` vs `actix-web` | `axum`/`tower` candidate | Existing routes, auth, headers, body caps, host checks, streaming, and shutdown win |
| SQLite/FTS5 | `rusqlite` bundled vs `sqlx` SQLite | Start with `rusqlite` behind store traits | Existing schema bytes, PRAGMAs, FTS5/tokenization, NULL/time encoding, locking, and WAL parity |
| PostgreSQL | `sqlx` vs `tokio-postgres` | `sqlx` candidate with Rustls | Existing schema/query/auth behavior and offline build reproducibility |
| Markdown/frontmatter | `noyalib` + focused lexical scanners vs `yaml_serde`/libyaml or a Markdown framework | Use pure-Rust `noyalib` behind fixture-tested conversion plus focused scanners | The Go parser's permissive delimiters/coercions, ordering, frontmatter types, tags and wikilinks are the contract; libyaml's large unsafe surface was rejected |
| Filesystem watch | `notify` | Candidate behind injected event source | Debounce/coalescing, overflow, rename, cancellation, and native-OS behavior |
| HTTP client | `reqwest` with Rustls | Candidate behind injected client | SSRF, redirects, proxy/env behavior, timeouts, and streaming limits |
| Mail | `mail-parser` + async IMAP candidate | Spike against MIME/IMAP fixtures before selection | Encodings, nested multipart, attachments, UID cursor, TLS, and retries |
| PDF | `lopdf`, `pdf-extract`, image/QR/barcode crates, external tools | No blanket selection; port operation by operation | Existing PDF bytes where stable, semantic pages/text otherwise, PDF/A and traversal limits |
| Crypto | `ed25519-dalek`, `sha2`, `hmac` | Use established RustCrypto crates | SymRoom signatures/member IDs/journal chains must cross-verify with Go |
| Async | Tokio | Only at HTTP/MCP/mail/process/cancellation boundaries | File/index/domain code stays synchronous unless measurements justify async |
| Errors/logging | `thiserror`, `anyhow`, `tracing` | Typed library errors; composition-only anyhow; stderr tracing | Existing public errors/exit codes and redaction remain exact |

Application crates use Rust edition 2024, `rust-version = "1.98"`, a committed
`Cargo.lock`, resolver 3, and `#![deny(unsafe_code)]`. Dependency versions are
reviewed and locked when the crate is first introduced, not guessed in this
proposal.

## 7. Contract and differential architecture

The neutral harness launches Go and Rust binaries with identical argv, cwd,
stdin, environment allowlist, locale, timezone, isolated HOME/XDG roots,
fixtures, ports, fake clocks, and process deadlines. It captures:

1. exit status or signal plus exact stdout/stderr;
2. recursive filesystem type/mode/hash manifests and symlink targets;
3. normalized Markdown semantic state and byte-exact files where documented;
4. SQLite schema, PRAGMAs, rows, NULL/time encoding, WAL state, and lock behavior;
5. PostgreSQL request/transaction behavior against an isolated service;
6. raw MCP frames, schemas, annotations, ordering, cancellation, and malformed input;
7. HTTP method/path/query/status/headers/body/stream transcripts;
8. spawned process argv/env/stdin/stdout/stderr, timeout, and process-tree cleanup;
9. signed SymRoom journal events and cross-language signature verification;
10. release archive members, modes, names, checksums, signatures, SBOMs, and installed smoke behavior.

Only temp roots, bound ephemeral ports, and explicitly fixed nondeterministic
fields may be normalized. Each normalization is named in the matrix. Generated
fixtures use production Go loaders and synthetic data; real user state is never
read.

Time-dependent parity does not rely on a fake `TZ` or timing luck. Before the
first lease, expiry, approval, snapshot, or retention fixture, the affected Go
package receives a test-only injectable clock whose production default remains
`time.Now`; Rust uses the matching narrow `Clock` trait. Fixtures then run both
implementations against the same fixed instants and advances. Until those seams
exist, the corresponding rows stay `TODO`.

## 8. High-risk seams

- SQLite compatibility: the repository has 45 migrations across multiple stores and relies on FTS5/tokenization, WAL, time parsing, and lock semantics.
- PDF/OCR/mail: parser differences can silently alter document content; every accepted format needs adversarial and corpus fixtures.
- The 200-path CLI: inherited flags must work before, between, and after nested commands where Cobra currently accepts them.
- MCP aliases: all 57 tools include legacy contracts that must remain listable/callable with exact annotations and schemas.
- HTTP security: bearer/worker tokens, path confinement, shares, host validation, size limits, and streaming must fail closed.
- SymRoom journal: Ed25519 signatures, Lamport ordering, JSON bytes, and conflict-free merge behavior are cross-language data contracts.
- External tools: Typst, Tesseract, Poppler, Ollama, and optional Symaira binaries need exact argv/env/timeout/fallback fixtures.
- Release composition: two CLI binaries, signed/notarized macOS artifacts, Docker, Home Assistant, Homebrew, and Swift consumers must move together only at cutover.

## 9. Quality and security gates

- `cargo fmt --all --check`
- `cargo check --workspace --all-targets --all-features`
- `cargo clippy --workspace --all-targets --all-features -- -D warnings`
- `cargo nextest run --workspace --all-features`
- `cargo test --workspace --doc --all-features`
- `cargo hack check --workspace --each-feature --no-dev-deps`
- `cargo llvm-cov nextest --workspace --all-features`
- `cargo audit` and `cargo deny check`
- nightly Miri for suitable core code
- nightly fuzzing for Markdown/frontmatter, query syntax, paths, archives, PDF/mail inputs, MCP frames, HTTP bodies, and room events
- native runtime CI on macOS, Linux, and Windows
- the complete Go gate remains green until final removal
- Swift macOS/iOS consumer builds remain green throughout

## 10. Cutover invariants

Cutover is blocked unless every applicable matrix row is `PASS`, the early and
full value gates pass without threshold changes, Rust-written data reopens in
Go, release artifacts and installed clients pass, and rollback has been
exercised from published prerelease artifacts. Any failure leaves Go as
production. That is not defeat; it is the migration plan doing its job.