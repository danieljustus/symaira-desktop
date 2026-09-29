# Symaira Desktop Rust Migration Implementation Plan

## Integrated raw MCP arguments and measured hybrid Ask — 2026-09-29

Candidate `2a00af4fd10022aae10ec8287eafba7523f16ecf` preserves the raw MCP
arguments through envelope decoding and shares Go-compatible string decoding
between Ask and Search. The actual Go registry/ServeIO fixture now has24 calls:
22 exact envelopes and2 explicitly unsupported notebook controls. It covers
folded and exact duplicate keys, null retention, first type errors, missing
arguments, duplicate arguments envelopes and duplicate top-level params.

The hybrid scenario indexes four synthetic documents, records one actual
`POST /v1/embeddings` request for a nonlexical query, and compares the exact
three fallback citations. Positive-zero search scores retain Go JSON bytes.
The provenance digest now binds all package-local cmd/internal Go tests,
helpers and testdata, replacing an incomplete manual generator allowlist.
The existing provenance test also works when Git has no installed templates.

Full immutable generation/application, ten Go/Rust differentials, inventory/
portgen tests and207 CLI/index/protocol tests pass on native Darwin/arm64 and
Linux/arm64, zero failed/ignored. Darwin strict Clippy/fmt/actionlint pass.
Both build roots retain `raw-arguments-receipt-2a00af4f.json`, binding the
candidate, metadata, fixtures and log hashes. Notebook scope, configured AI
providers, HTTP hybrid Ask and other native targets keep AI-002/RUST-012 open.
No live provider, publication or installed cutover was used.


## Integrated bounded offline Ask MCP — 2026-09-29

Candidate `bd5fb252627c1a008622dda71f1bf8c749325dc3` exposes desk_ask through
MCP and reuses the existing offline Ask/search helpers. The real Go tools
registry and ServeIO oracle supplies the catalog and11 call envelopes for
one tag-search scenario. Rust matches8 call envelopes (including case-folded
keys, null, whitespace and field type errors). Two folded-duplicate cases
currently reject instead of Go last-write-wins; the notebook case explicitly
reports unsupported scope. Those3 controls are declared differences.

Full immutable generation/application, ten differentials and206 CLI/index/
protocol tests pass on native Darwin/arm64 and Linux/arm64, zero failed or
ignored. Darwin strict Clippy/fmt/actionlint pass. Both build roots retain
`ask-mcp-receipt-bd5fb252.json` with source, fixture, metadata and log hashes.

This fixture does not exercise hybrid MCP Ask: its empty provider_requests
array is assigned, not measured. Existing CLI/search hybrid tests are separate
evidence. Raw argument ordering, a measured hybrid MCP Ask case, notebook
scope, configured AI providers, HTTP hybrid Ask and remaining native targets
keep AI-002/RUST-012 open. No live provider or installed cutover was used.


## Integrated inert retrieval settings — 2026-09-29

Candidate `bacaaff839a925b0daba945d914475f28ed14ebe` removes Rust-only rejection
of vector-backend and quantization settings in shared hybrid search. Actual
Go production openClientAt wires neither setting into its engine. New real
CLI and MCP cases preserve output and embedding requests for turbo-prod and
unknown backend/quantization values loaded from TOML. No dormant alternate
backend or TurboQuant implementation is activated.

Full immutable generation/application, nine actual Go/Rust differentials and
205 CLI/index/protocol tests pass on native Darwin/arm64 and Linux/arm64 with
zero failures or ignored tests; Darwin strict Clippy/fmt/actionlint also pass.
`inert-settings-receipt-bacaaff8.json` in both build roots binds source, metadata,
fixtures and log hashes. Remaining facades, format repair, dormant algorithm
contracts and other required native targets keep INDEX-005/RUST-008 open.


## Integrated offline Ask CLI retrieval — 2026-09-29

Candidate `a427ce7b0693d30b17b0b8c85003f9479810da93` connects the existing
shared hybrid retrieval and SearchPlan paths to the no-provider Ask CLI. The
Go Service.Ask oracle and actual Rust CLI compare seven cases: more than three
citations with three fallback links, a registered external source, semantic
hits without lexical matches, empty results, missing-index sidecar fallback,
empty input and a tag operator. Captured synthetic embedding requests prove
which retrieval path ran; operator and empty input cases make no provider call.
Only exact temporary roots are normalized. Scores and event content are not masked.

Full immutable fixture generation and reviewed application passed. Independent
clean Darwin/arm64 and Linux/arm64 runs pass nine Go/Rust differentials and all
205 CLI/index/protocol tests, zero failed or ignored. Darwin strict Clippy,
formatting and actionlint pass. Receipts `ask-offline-receipt-a427ce7b.json` in
the existing Desktop and native-linux build roots retain source metadata and
fixture/log hashes.

INDEX-005, AI-002, RUST-008 and RUST-012 remain open. Configured AI providers,
MCP Ask, HTTP hybrid Ask, other required native targets and complete ledger
acceptance are not established by this slice. The prior stricter Rust query
root allowlist is retained. No live provider/model, publication, installed
cutover or Go removal occurred.


## Integrated inert rerank configuration parity — 2026-09-29

Candidate `3d4d8a569e4da39bb431e97e30f9198927c98a39` removes Rust's rejection
of `rerank_query=true`. Go's actual CLI/MCP retrieval client never supplies a
RerankCfg to the engine, so that flag is currently inert on both surfaces.
The migration preserves this behavior; it does not activate the dormant Go
reranker or introduce provider calls.

Two actual Go fixture cases cover the configured flag through CLI search and
MCP search, asserting an embeddings-only request trace and unchanged results.
Real Rust processes replay both cases. Full immutable generation and reviewed
artifact apply passed. Independent clean Darwin/arm64 and Linux/arm64 gates
pass eight Go/Rust differentials and all204 CLI/index/protocol tests with zero
failures/ignored. Darwin strict Clippy, fmt and actionlint pass. Both build roots
contain `rerank-config-receipt-3d4d8a56.json` with source/fixture/log identity.

INDEX-005/RUST-008 remain open for Ask and other service facades, formats,
quantization and required native targets. No publication, installed cutover,
Go removal or live model request occurred.


## Integrated local HyDE query expansion — 2026-09-29

Candidate `ce97dcf18aa60118a78fde84cf23c9884429ce18` sends Go's HyDE prompt
through a bounded local Ollama chat request, trims the generated passage with
Go's rune/word boundary rules and averages its embedding with the original
query vector. CLI and MCP share this implementation. Failure preserves the
original vector; lexical search still uses the original query.

Actual Go service/ServeIO fixtures and real Rust processes cover successful
expansion, failure, identical-text embedding-cache reuse, an initially unknown
embedding dimension with unequal returned vectors, the first 512 error bytes,
and Go chat JSON case/null/duplicate/trailing-document behavior. The transport
uses loopback HTTP only and caps a successful chat body at 4 MiB; this bound is
stricter than Go's unbounded decoder. No live model or provider was contacted.

Complete immutable fixture generation and reviewed artifact application passed.
Independent clean Darwin/arm64 and Linux/arm64 gates then passed eight Go/Rust
differentials and all 204 CLI/index/protocol tests, zero failed/ignored. Darwin
strict all-target Clippy, formatting and actionlint passed. Both build roots
contain `query-expansion-receipt-ce97dcf1.json` with candidate/source metadata,
fixture hashes and log hashes. The Go production digest remains `9928661e...`.

INDEX-005/RUST-008 remain open for remaining facades, formats, quantization and
native targets. Go's actual CLI/MCP path currently ignores rerank configuration;
Rust still rejects that option in this candidate, to be corrected separately
without activating a new Go feature. No publication, installed cutover or Go removal.


## Integrated shared query plan and MCP hybrid search — 2026-09-29

Candidate `7e65075f1cd183452773ad664f81086dc1927747` connects `desk_search`
to the same hybrid query/embedding path used by the CLI. Parsed filters and regex
queries use a shared sidecar SearchPlan executor; malformed syntax preserves the
plain-search hint. Actual Go `ServeIO` handler envelopes replay through a real
Rust MCP subprocess in ten cases, including metadata, source roots and retained
rows for negated path/status/type. Snippets are compared unchanged.

Review found and corrected two defects: singleton SQL filters were applied again
with placeholder post-filter results, discarding negated-filter survivors; and
fallback search omitted registered external sources. Both paths now reuse the
vault/registered-source root helper. Unregistered rows are excluded, intentionally
more strictly than Go's unscoped `DB.SearchPlan`; a separate negative control makes
that boundary explicit. Unix root prefixes preserve a legal trailing backslash.

Full immutable generation, artifact review and validated application passed.
The production Go digest remains 9928661e32e94a46528dc10b54d6b6fbdebd1915a530fd032ad4b87e84ee2fb3.
Independent clean Darwin/arm64 and Linux/arm64 gates pass eight Go/Rust
differentials and all 200 CLI/index/protocol tests, zero failures or ignored tests.
Darwin strict Clippy, fmt and actionlint pass. Receipts
`search-mcp-receipt-7e65075f.json` in the Desktop/native-linux build roots retain
worktree metadata, source/fixture digests and log hashes.

INDEX-005/RUST-008 remain open for expansion, reranking, quantization, other
formats/facades and remaining native platforms. Existing CI now invokes the MCP
differential; no remote run, publication, cutover or Go removal is claimed.

## Integrated CLI hybrid search checkpoint — 2026-09-29

Candidate `4bf26cab4c065611f5bab2f6c6c215fd3f947481` connects plain CLI
search to the existing hybrid retrieval engine. Scoped queries retain sidecar
search. Query vectors are reused across source roots; dimensions, mixed embedding
spaces, local-hash fallback, path confinement, titles, metadata matches and Go
snippet projection are exercised through the real Rust CLI against eight actual
Go `Service.SearchWithMeta` cases. A partial Kelvin-sign byte prefix proves Go's
per-invalid-byte rune counting. Only equal-score tie order is normalized.

Full immutable generation and reviewed fixture application pass. Production Go
source digest remains `9928661e32e94a46528dc10b54d6b6fbdebd1915a530fd032ad4b87e84ee2fb3`;
generator digest is `4261248ce9be8a78c806064a7692c90c3bae6edb4f74c4d64fdef8bd56aeca00`.
Search fixture SHA256 is
`3a3199c0bfa412f98eb840e4b5f0020e7bb0c28217adc8d1cc9a538baa506f12`.

Independent clean-candidate Darwin/arm64 and Linux/arm64 each pass seven Go/Rust
differentials and all197 CLI/index/protocol tests, zero failed or ignored. Darwin
strict Clippy, formatting and workflow validation pass. Source-path metadata,
log hashes and receipts `search-cli-receipt-4bf26cab.json` remain in the existing
Desktop and native-linux build roots. Native CI includes the new differential;
no remote run is claimed. MCP/ask facade wiring, expansion, quantization, other
formats and remaining native platforms keep INDEX-005/RUST-008 open. Unsupported
retrieval settings fail explicitly. No publication, installed cutover or Go
removal occurred.


## Integrated local-hash embedding checkpoint — 2026-09-29

`06446c614699583863394f7fddba696afc50bfb4` adds the Go-compatible local-hash
primitive and its source-bound oracle to the preceding retrieval/repair stack.
Ten actual Go vectors match Rust by exact float32 bits: empty/stopword inputs,
punctuation, collisions, Unicode whitespace/case, positions beyond32 and the
768-dimension default. Zero dimensions fail explicitly instead of Go's panic.
A digest over every Unicode scalar's simple lowercase mapping found55 mappings
introduced after the pinned Go1.26.6 Unicode15 table; exact identity overrides
preserve Go behavior, and the complete mapping digest now passes.

Full immutable fixture generation and reviewed application pass. Go production
source digest stays `9928661e32e94a46528dc10b54d6b6fbdebd1915a530fd032ad4b87e84ee2fb3`;
generator digest is `d474c91b39d21487eafa06d17a934bab9cd81047de089e482aeb3203dae8c6f5`,
and local-hash fixture SHA256 is
`8826522fe8f00757aeca3f4250eec042985c3eac2feca7740981bd2656aeb2d1`.
The generation/apply artifact and logs are retained under the existing Desktop
build root. Core/vault fixture baselines remain unchanged.

Independent clean-candidate Darwin/arm64 and Linux/arm64 each pass six Go/Rust
retrieval differentials and all194 CLI/index/protocol tests, zero failed/ignored.
Darwin strict Clippy, formatting and workflow checks pass. Receipts
`local-hash-receipt-06446c61.json` are in the existing Desktop/native-linux build
roots. CI includes the native local-hash differential; no remote candidate run
is claimed. This is the primitive only: query caller wiring, complete retrieval
facade and remaining native platforms keep INDEX-005/RUST-008 open. No publication,
installed cutover or Go removal occurred.


## Integrated UTF-8 text repair checkpoint — 2026-09-29

`f0704449b27189e46544af75d2bd48f7b20268b9` extends the Markdown checkpoint
below with bounded UTF-8 `.txt` pending repair. Six Go/Rust HTTP CLI cases and
all 191 CLI/index/protocol tests pass on native Darwin/arm64 and Linux/arm64,
zero failed/ignored. Complete immutable provenance and Darwin strict lint gates
pass. Invalid UTF8, other document formats/providers and remaining platforms
remain open; no whole ledger row is promoted.


## Integrated pending Markdown repair checkpoint — 2026-09-29

`058ff6d7d95fff96232287601182f4bf09681d00` passes the five Go/Rust retrieval
differentials and all 188 CLI/index/protocol tests on native Darwin/arm64 and
Linux/arm64. The reembed CLI fixture covers success, retry, provider failure,
learned dimensions and mismatches. Full immutable provenance, Darwin strict
Clippy/fmt/actionlint also pass. See the contract matrix's integrated Markdown
repair section for the intentional incomplete-result behavior and remaining
provider/parser/facade/platform limits. No whole ledger row is promoted.


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