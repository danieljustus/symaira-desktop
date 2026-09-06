# Symaira Desktop Rust Migration Implementation Plan

> **For implementers:** work strictly in dependency order from
> `work-items.json`. Use separate branches/worktrees for independent items; keep
> parity-sensitive dependent slices under one coordinator.

**Goal:** Replace the Go `symdesk` and `symroom` backends with idiomatic safe
Rust only if executable parity and the measured value gates justify cutover.

**Architecture:** Keep Go as a black-box oracle. Add Rust crates only when their
first vertical slice starts. Every slice begins with Go-generated fixtures and
ends with Go↔Rust differential evidence. Swift remains a consumer surface.

**Initial stack:** Rust 1.98 / edition 2024, repository-local Cargo workspace,
clap, serde, thiserror, tracing, candidate rusqlite/sqlx/axum/rmcp adapters,
nextest, proptest, insta where byte snapshots are appropriate, llvm-cov, audit,
deny, Miri, cargo-fuzz, and native CI.

## Global execution protocol

For every item:

1. Verify the worktree branch and read affected Go code/tests plus its contract rows.
2. Generate the Go-oracle fixture through production code or black-box binaries; never hand-author behavior that can be generated.
3. Prove fixture drift or an intentional mismatch fails loudly, then revert the mismatch.
4. Add the smallest Rust behavior that consumes the same fixture.
5. Run focused Rust tests, Go tests, and the differential case.
6. Run format, check, Clippy, nextest, doctests, feature, audit, and deny gates affected by the slice.
7. Update matrix rows only when executable CI evidence exists.
8. Update exactly one work item and unblock only direct successors whose dependencies passed.
9. Do not use real vaults, credentials, keychains, mailboxes, AI/OCR services, or remote servers.
10. Stop on unexplained drift, security regression, destructive data mismatch, or a failed value gate.

## Stage 1 — Oracle before Rust

### RUST-001: Freeze the Go oracle and neutral harness

Create a language-neutral harness under `scripts/rust-port/` and generated
fixtures under `testdata/port/`.

- Export all 206 observable SymDesk command paths, including Cobra's generated help/completion tree, with hidden state, aliases, groups, arity, local/persistent flags, defaults, and help.
- Export the complete hand-written SymRoom command/action grammar.
- Export all 57 SymDesk and 8 SymRoom MCP tools including order, schemas, aliases, and annotations.
- Export all 21 HTTP route method/path patterns.
- Capture status, signals, bounded raw streams, recursive filesystem manifests, and raw stdin/stdout protocol frames in the foundational harness. Add SQLite snapshots, loopback HTTP recording, and injected child-process transcripts before the first later slice that claims those comparison modes; `RUST-001` does not mark their matrix rows as passing.
- Isolate HOME/XDG, cwd, locale, timezone, temp roots, PATH, and environment; later network/time slices must inject ephemeral ports and fixed clocks through their adapters.
- Add `make port-fixtures-check` and `make differential-go-selftest` without changing production behavior.

**Acceptance:** Go self-comparison passes; mutating one golden byte fails; the
existing Go test/lint/build gates remain green.

### RUST-002: Initialize Rust workspace and exact version slices

Only after RUST-001 passes:

- Pin Rust 1.98 with rustfmt and Clippy; edition 2024, resolver 3, explicit `rust-version`, Apache-2.0, committed lockfile, and deny policy.
- Create only `symdesk-core`, `symdesk-cli`, and `symroom-cli` because both version commands are exercised immediately.
- Implement byte-exact `version`, `--version`, JSON/error cases for both binaries.
- Add standard Cargo gates and native macOS/Linux/Windows CI without weakening Go or Swift CI.
- Record a clearly non-representative version-only size/startup signal.

**Acceptance:** both Rust version slices pass exact differential tests and every
Rust repository gate; Go remains production.

## Stage 2 — Representative slice and early stop gate

### RUST-003: Pure core, config, and query contracts

- Freeze output/error enums, config defaults/precedence, text normalization, search grammar, date/range parsing, hashing, and SimHash.
- Port deterministic logic into `symdesk-core` with explicit domain types.
- Add property/fuzz coverage for query/config/path-like parsers and Miri for suitable code.

**Acceptance:** focused fixtures pass without Tokio, HTTP, MCP, SQLite, or
external-process dependencies.

### RUST-004: Read-only Markdown vault

- Generate full/minimal contract-v1–v6 fixtures through Go loaders, including unknown-field preservation, iOS v2-compatible minimal writes, v5 scalar/list aliases and bases, v6 datasets and hybrid metadata, plus malformed, Unicode, wikilink, attachment, notebook, and view cases.
- Port read-only walking/parsing/resolution into `symdesk-vault`.
- Add hidden-directory, symlink, traversal, case, size, and malformed-input tests.
- Do not add Rust write paths yet.

**Acceptance:** Rust semantic snapshots equal Go for the full synthetic corpus;
fuzz/property tests are bounded and no live vault is read.

### RUST-005: Minimal sidecar, index, and search

- Freeze migrations, PRAGMAs, FTS5 tokenizer/ranking/snippets, timestamps/NULLs, and lock behavior.
- Create `symdesk-index` with only the sidecar migration/open/index/search paths needed for a representative vault.
- Prove Go-created databases open in Rust and Rust-created databases reopen in Go.
- Compare full index, incremental/no-op update, delete, corruption, read-only, and busy cases.

**Acceptance:** deterministic 10k-vault index/search output and persisted state
match; existing database copies round-trip both directions.

### RUST-006: Representative CLI + MCP + HTTP and early value gate

- Add only `ls`, `search`, and read-only `desk_status`/`desk_ls`/`desk_search` plus `/healthz`, `/api/v1/status`, snapshot, and read-file HTTP paths.
- Spike `rmcp`, `axum`, and `rusqlite` behind compatibility adapters; retain them only if raw contracts pass.
- Exercise malformed frames/bodies, auth failures, path confinement, stream hygiene, cancellation, and shutdown.
- Measure the representative SymDesk Go/Rust pair on the same machine: SymDesk binary size, long-running RSS, startup p95, search p95, MCP p95, and HTTP p95 with at least 100 post-warmup samples. The partial SymRoom version binary is excluded from this early gate.
- Apply the threshold in `baseline-20260906.json` unchanged.

**Acceptance:** representative contract rows pass and `VALUE-001` passes.
Failure marks the migration `stopped`; Go remains production and later stages do
not start.

## Stage 3 — Complete local data capabilities

### RUST-007: Vault writes, history, trash, conflicts, and retention

Port atomic create/edit/move/delete, properties/tags/docs, notebooks/views/
datasets file updates, history/checkpoint/undo, trash/restore/purge, conflict
copies, and retention proposal/accept/reject. Every Rust write must reopen and
mutate correctly in Go before the slice passes.

### RUST-008: Full sidecar and retrieval

Port index lifecycle backups/relocation/retry/status, retrieval migrations,
chunking/anchors, BM25/RRF/vector and keyword-only fallback, quantized sidecars,
embedding backend adapters, and large graph behavior. Declare numeric tolerances
before using them; preserve deterministic ordering.

### RUST-009: Contacts and datasets

Port contacts/vCard/CSV, relationships/security/memory links, dataset materialized
rows, provenance/idempotency, views, grouped aggregates, and sensitivity gates.
Preserve the reference-only contact boundary exposed outside the crate.

## Stage 4 — Document pipelines

### RUST-010: Ingest queue, extraction, OCR, mail, and importers

- Port ingest-store migrations and queue/lease/retry state machine first.
- Port type detection and bounded extraction against a sanitized fixture corpus.
- Add full subprocess traits for Tesseract, Poppler, Ollama, and helpers; fixtures capture argv/env/stdin/stdout/stderr/timeouts and process cleanup.
- Port MIME/IMAP cursor/rules behavior against fake servers.
- Port Paperless/Notion import, classification, storage-path templates, provenance, and idempotency.
- Fuzz mail, archive, metadata, and untrusted extraction boundaries.

### RUST-011: PDF, archive, draw, and export

Port operations one at a time rather than choosing one PDF crate for everything:

1. split/merge/rotate and hostile-PDF limits;
2. text/metadata extraction required by ingest;
3. archive/PDF-A validation and external Typst contract;
4. draw parser/IR/layout/font metrics/emit;
5. HTML/CSV/PDF exports and profiles.

Use semantic PDF checks where Go bytes are nondeterministic. Golden SVG/JSON/CSV
remain byte-exact where the Go output is deterministic.

### RUST-012: AI, composition, recipes, and external tools

Port Ollama/provider config, ask/transform/citations/notebook scope, streaming,
optional Symaira PATH probes, result externalization, recipes, and graceful
fallbacks. Every side effect goes through injected process/HTTP/clock traits;
partial delegation that bypasses test doubles is rejected.

## Stage 5 — Complete product surfaces

### RUST-013: Complete SymDesk service and CLI

- Port remaining service use cases and all 206 observable command paths by user-visible family.
- Freeze and test inherited flags at every accepted argv position, not only generated tree metadata.
- Preserve output modes, exit taxonomy, completions, events, signals, process cleanup, and remote command allowlist.
- Run full Go↔Rust CLI permutations plus macOS/Linux/Windows smoke tests.

### RUST-014: Complete SymDesk MCP

- Snapshot read-only and read-write registries and all legacy aliases.
- Port all 57 tools, schemas, order, annotations, call-time write enforcement, externalized results, errors, bounds, cancellation, and zero-stdout-pollution behavior.
- Run official MCP conformance plus raw Symaira differential/property/fuzz suites.

### RUST-015: Complete authenticated self-hosted HTTP

- Port all 21 routes: tokens, files/snapshots/command, workers/jobs/ingest, AI/notebooks, permissions/shares, health, limits, TLS/Host policy, and graceful shutdown.
- Port PostgreSQL behavior against an isolated service and preserve SQLite mode.
- Verify Docker and Home Assistant modes without touching production deployments.

### RUST-016: Port SymRoom

- This item cannot start until `RUST-006` and `VALUE-001` pass; the DAG enforces that barrier.
- Freeze and port Ed25519 identity/member IDs, canonical signed events, journal merge/verify, derived index, membership, approvals, runs/checkpoints, artifacts, watch, profiles, and doctor.
- Port all CLI parser behavior and 8 MCP tools.
- Require Go↔Rust cross-signature verification and two-way journal/index rollback.

## Stage 6 — Reversible release

### RUST-017: Full value gate and dual-binary prerelease

- Build Go and Rust release candidates from clean and warm caches on the same host.
- Collect paired distributions for startup, representative CLI/index/search/graph/MCP/HTTP/ingest workloads, long-running RSS, and binary/archive size.
- Reapply both original thresholds; do not redefine metrics after seeing results.
- If green, modify `.goreleaser.yml`, `.github/workflows/release.yml`, Homebrew packaging, Docker and Home Assistant entrypoints to package/select Rust `symdesk`/`symroom` plus exact fallback names `symdesk-go`/`symroom-go`.
- Verify every prerelease archive contains exactly those four executables plus the existing documentation/license payload, and exercise explicit direct fallback invocation before publishing.
- Verify target archives, checksums, Cosign, SBOM/provenance, codesigning/notarization, Homebrew, Docker, Home Assistant, and public artifact bytes.
- Exercise rollback against Rust-written copied data.

### RUST-018: Stable cutover with Go fallback

Run native platform suites and Swift macOS/iOS consumer tests against installed
release candidates. Publish one stable Rust-primary release that still contains
the frozen Go fallbacks. Operate it without unexplained parity defects before
allowing final removal.

### RUST-019: Delayed Go removal

In a separate reviewed change after RUST-018's operating period:

- tag the immutable final dual-binary rollback release;
- remove backend Go source, `go.mod`/`go.sum`, Go-only CI and GoReleaser assumptions;
- retain Swift sources and all external contracts;
- prove zero tracked backend `.go` files, full Rust/Swift gates, installed release smoke, data compatibility, and fallback artifact availability.

## Completion contract

The migration is complete only when every applicable matrix row is `PASS`, both
value gates pass, published artifacts are verified, Swift consumers pass, and
Go rollback has been exercised. A finished checklist without those artifacts is
not a finished port.