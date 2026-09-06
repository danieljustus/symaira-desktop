# Go-to-Rust migration record

> **Status:** implementation active; `RUST-001` through `RUST-003` passed
> **Go oracle:** commit `ae86331930fdfa2b128b68ae5af7437091b9949a`, release `v0.12.2`
> **Scope:** the Go `symdesk` and `symroom` backends; SwiftUI clients and Swift packages stay Swift
> **Tracking:** [#852](https://github.com/danieljustus/symaira-desktop/issues/852)

## Decision

Symaira Desktop will be evaluated for an in-place, contract-first Rust migration.
The Go implementation remains the production binary and executable oracle until
all applicable contract rows pass. There is no flag-day rewrite, no cross-repo
Cargo workspace, and no empty Rust scaffold in this preparation change.

This is a high-risk port: 749 Go files and 167,922 lines, 206 observable `symdesk` command
paths, 57 `symdesk` MCP tools, 21 self-hosted HTTP routes, 8 `symroom` MCP tools,
45 SQLite migration files, PostgreSQL support, OCR/mail/PDF pipelines, two
release binaries, and macOS/Linux/Windows release artifacts. The native macOS
and iOS applications are not part of the language migration.

## Why Rust — and the stop rule

The intended measurable gain is a smaller or lower-RSS long-running server/MCP
process, predictable latency without garbage-collector pauses, and one
memory-safe systems implementation for the untrusted protocol, archive, PDF,
mail, path, and database boundaries. Go is already memory-safe; Rust does not
justify this rewrite merely by existing.

The port continues beyond the representative read/index/search/MCP/HTTP slice
only if a release-profile Rust candidate demonstrates at least one of:

- at least 20% lower SymDesk maximum RSS under the representative workload; or
- at least 20% smaller SymDesk release binary;

while startup p95, indexed-search p95, HTTP p95, and MCP p95 regress by no more
than 10%. Security and contract parity are mandatory even when the performance
gate passes. Failure keeps Go in production and stops the port instead of moving
the goalposts.

The early gate excludes the version-only SymRoom candidate; combined
`symdesk`+`symroom` size and RSS are evaluated only by the full `VALUE-002`
gate after the real SymRoom implementation exists.

The measured Go baseline is in
[`baseline-20260906.json`](baseline-20260906.json).

## Non-negotiable constraints

1. Existing Markdown vaults remain readable and writable without a content migration.
2. Markdown remains the source of truth; every SQLite store remains derived or retains its current documented role.
3. Existing sidecar, retrieval, contacts, ingest, room-index, and server data reopen without destructive migration; Go rollback remains possible.
4. CLI argv behavior, output modes, exit codes, MCP frames/tool schemas, HTTP API, auth, events, file modes, and release asset names remain compatible.
5. MCP stdout contains protocol frames only; diagnostics stay on stderr.
6. Go remains buildable and testable until one stable Rust release has operated without unexplained parity defects. Go removal is a separate final change.
7. `#![deny(unsafe_code)]` is the default. Exceptions need a written safety invariant, focused tests, Miri where applicable, and review.
8. macOS arm64/amd64, Linux arm64/amd64, and Windows arm64/amd64 remain supported unless a separate compatibility decision removes a target.
9. SwiftUI macOS/iOS apps, `meet/`, and `room/client/` stay Swift. They are verified as consumers, not rewritten for language purity.
10. Separate Symaira products remain runtime integrations. This repository does not become a cross-repository Rust workspace.
11. No production vault, account, token, mail server, keychain, or external OCR/AI service is used by fixtures.

## Prepared artifacts

- [`architecture.md`](architecture.md) — target crate boundaries, dependency decisions, risks, and rollback.
- [`contract-matrix.md`](contract-matrix.md) — acceptance map for observable behavior.
- [`implementation-plan.md`](implementation-plan.md) — ordered vertical slices and gates.
- [`work-items.json`](work-items.json) — machine-readable dependency graph.
- [`baseline-20260906.json`](baseline-20260906.json) — measured Go reference metrics.
- [`value-signal-version-20260906.json`](value-signal-version-20260906.json) — non-representative first Rust slice measurements.

## Implementation progress

- `RUST-001` passed: generated fixtures freeze 207 SymDesk command nodes (206
  non-root, including Cobra's generated help/completion tree), the production-derived SymRoom parser grammar, 57 SymDesk and 8
  SymRoom MCP tools, and 21 HTTP routes. The neutral harness compares both Go
  binaries in isolated sandboxes and its explicit same-binary 26-case Go
  self-test is green. Real differential runs reject identical binaries unless
  that self-test override is passed.
- Production-source provenance covers Go source, embedded assets and migrations,
  the vault contract, and release inputs. Fixture checksums and source drift are
  executable CI gates.
- `RUST-002` passed: the Rust 1.98 workspace contains only `symdesk-core`,
  `symdesk-cli`, and `symroom-cli`; 17 Go↔Rust version cases pass byte-for-byte,
  together with format, Clippy, nextest, doctest, feature, coverage, audit, deny,
  and local macOS gates. Linux/Windows native gates are configured in CI; their
  matrix rows remain `TODO` until CI executes them.
- `RUST-003` passed locally: Go-generated fixtures and safe Rust parity are
  green for SimHash, document-format policy, OCR dehyphenation/language hints,
  German FTS/trigram normalization, and the complete search-query/date parser
  (22 query and 17 date cases). Unified configuration parity covers defaults,
  supported and currently ignored environment overrides, ordered validation,
  base XDG/HOME paths, secret-safe state, unknown TOML keys, malformed input,
  and byte-exact Go encoder output. The full `symdesk-core` slice passes Miri.
  Configuration fixtures must pin the four tagged-but-currently-ignored
  environment variables tracked in [#854](https://github.com/danieljustus/symaira-desktop/issues/854), not silently fix them in Rust.
  Go remains production.
- `RUST-004` is in progress: a new `symdesk-vault` crate passes 34
  Go-generated `ParseBytes` cases covering contract v1–v6, YAML coercions,
  unknown nested fields, exact SHA-256/size/body bytes, all type inference,
  ASN errors, aliases, tags, wikilinks, CRLF and Excalidraw behavior. Its own
  code and the pure-Rust `noyalib` YAML parser contain no unsafe expressions;
  the complete fixture suite also passes Miri. Native walk/ignore behavior,
  lowercase-only `.md` selection, symlink entries, Go-compatible `SecurePath`,
  and graph target precedence (path → basename/title → alias) now pass separate
  Go-generated fixtures; a Windows-specific walk/confinement test compiles for
  the native CI lane. Attachment-health resolution now passes a 17-case Go
  fixture. The pinned frontmatter fuzzer found and minimized an invalid-UTF-8
  tag-scanner panic; #858 tracks the local fix and regression test, and a
  10,000-run smoke gate now runs in CI.
  Typed read-only loaders for notebook, base/view/property definitions and
  contract-v6 dataset handles (including legacy policy defaults and invalid
  policy rejection) now pass Go-generated fixtures. The canonical hybrid
  metadata representation, matching/stripping behavior and Unicode paths,
  titles, aliases and attachments are covered as well. An actual deterministic
  `MobileNoteWriter` document is parsed by Swift, Go and Rust. RUST-004 remains
  `in_progress` until the Linux/Windows native CI lanes execute these contracts.

## Reuse assessment

Reconnaissance supports crate-level reuse, not adoption of another product:

- `clap` is the CLI candidate, but Cobra and the hand-written `symroom` parser remain the behavior oracle.
- `rmcp` (the official Rust MCP SDK) is the first MCP candidate behind a compatibility adapter; raw-frame parity decides whether it stays.
- `axum`/`tower` are the HTTP candidates; existing auth, limits, streaming, host/origin checks, and shutdown behavior win over framework defaults.
- `rusqlite` with explicit bundled/FTS5 configuration is the initial SQLite candidate; persisted database and locking parity are required before selection is final.
- `sqlx` with Rustls is the PostgreSQL candidate for the self-hosted store.
- `notify`, `reqwest`, `mail-parser`, `ed25519-dalek`, `lopdf`, and Rust-native barcode/QR/image crates are candidates, not accepted substitutes until their contract slices pass.
- Existing Rust Markdown-vault/MCP projects such as TurboVault provide design evidence but do not cover SymDesk's CLI, storage, ingest, server, PDF, dataset, room, and release contracts.

The correct strategy is a repository-local Cargo workspace plus language-neutral
fixtures. Copying a third-party vault product would replace one rewrite risk with
several compatibility risks wearing a trench coat.

## Execution rule

Start at the first `ready` item in `work-items.json`. Complete its fixture,
differential, security, and platform gates; update the matrix and exactly one
work item; then unblock direct successors. Stop on unexplained parity drift or a
failed value gate. Never silently normalize a mismatch.