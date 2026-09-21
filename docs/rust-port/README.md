# Go-to-Rust migration record

> **Status:** implementation active; `RUST-001` through `RUST-005` passed; `RUST-006`, `RUST-007`, and `RUST-016` are blocked by the current VALUE-001 order-bias gate ([#936](https://github.com/danieljustus/symaira-desktop/issues/936)).
> **Go behavior oracle:** commit `745c08e8144971c61133c5d0e5d61c7ce405aad2`, release reference `post-v0.12.2-security-880`; portgen provenance instead records the revision whose production source the fixtures were generated from, which must be the checked revision or one of its ancestors. Those are distinct identities by the current contract; their long-term consolidation is tracked in [#934](https://github.com/danieljustus/symaira-desktop/issues/934). VALUE baselines remain pinned to `ae863319` / `v0.12.2`
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
- [`value001-result.schema.json`](value001-result.schema.json) — schema for measured VALUE-001 artifacts.
- [`value001-order-bias-repair.md`](value001-order-bias-repair.md) — schema-4/5 order-stratified decision contract and fail-closed boundary.
- [`value001-fbc52d0c-current-gate.md`](value001-fbc52d0c-current-gate.md) — current-candidate decision; no RUST-006/RUST-007/RUST-016 advancement.
- [`results/value001-retained.json`](results/value001-retained.json) and its provenance sidecar — historical privacy-reviewed 655d248 capture; check historical evidence with `make value-001-evidence-tests`, not as approval of current HEAD. Exact candidate approval requires the explicit command in [`operations-gate-checkpoint.md`](operations-gate-checkpoint.md).
- [`value-signal-version-20260906.json`](value-signal-version-20260906.json) — non-representative first Rust slice measurements.

## Running VALUE-001

`make value-001` builds stripped Go and Rust release-profile `symdesk`
artifacts, runs the representative CLI/MCP/HTTP differential contracts first,
then records paired startup, indexed-search, MCP, HTTP, and long-running RSS
samples. The harness requires at least 100 post-warmup samples per workload,
uses a synthetic vault and loopback-only dynamic ports, records raw samples,
and exits non-zero unless contract parity, the 20% binary-size-or-RSS gate, and
all declared latency gates pass. Override `VALUE_OUTPUT` to retain a separate
artifact; the default is `docs/rust-port/results/value001-latest.json`.
The Make targets default all temporary homes, XDG roots, language caches, and
Python bytecode to the attached NVMe; override `VALUE_RUNTIME_ROOT` only with
another external build volume.

The timed `desk_ls` MCP call names each side's own absolute `cohort-042`
directory (`dir` is matched against the indexed absolute paths), so it returns
one deterministic 100-document cohort inside the SEC-003 1 MiB
outgoing-response limit instead of the full listing, which the Rust port
rejects at that limit. Index preparation, CLI listing, and search still use
the full 10,000-document vault.

### Current acceptance

RUST-006 is accepted for the exact candidate
`5c5e98c5aab2df54bb2c47ad51fe3d2d8e71f23b`: one fresh schema-6 run (100 samples,
20 warmups, unchanged oracle `745c08e8…`) passed all four differential
contracts, the ≥20 % improvement criterion (binary size −84.80 %, representative
RSS −71.62 %) and the interval latency gate. The published capture is
[`results/value001-5c5e98c5.json`](results/value001-5c5e98c5.json) with its
`.metadata.json`; `python3 scripts/rust-port/validate_value001_5c5e98c5.py --raw <private capture>`
re-proves that it is exactly the reviewed redaction of the private raw capture
(SHA-256 `40267a5c…`). RUST-007 and RUST-016 are `ready`; Go remains production.

## VALUE-001 units and display

VALUE-001 stores reductions and relative latency regressions as dimensionless
ratios, not percentage values. A regression is `candidate / reference - 1`, so
`0.10` is the unchanged **+10%** latency ceiling and gate comparisons use
`value <= 0.10` directly. Schema 2 retains its historical
`thresholds.p95_regressions`; schema 3 retains its declared pooled estimator
and provenance. Schema 4 remains historical order-stratified evidence; schemas 5
and 6 are the controlled HTTP order-stratified formats. Both require
`order_stratified_paired_median_ratio`, record each execution-order cohort in
`thresholds.latency_order_regressions`, and store the worse cohort as
`thresholds.latency_regressions` for each gate. The stratified median intervals
remain separate by order under
`thresholds.latency_order_regression_intervals`; pooled intervals are never a
decision input. Schema 5 also rotates HTTP routes per round and
alternates Go/Rust independently for each route, so no route is permanently
first in a server burst. Missing, mismatched, or materially unbalanced
`go-rust`/`rust-go` labels fail closed. Historical pooled evidence remains
auditable but cannot approve a new candidate.

Only the current schema can approve a candidate. Schema 6 keeps schema 5's
schedule and statistics but decides the latency gate on the stratified
**interval** instead of the point estimate: a cohort fails when its interval
lower bound exceeds `maximum_latency_regression`, so a cohort whose estimate is
above the ceiling but whose interval still contains it does not decide the gate.
The same rule fails closed when an interval is wider than
`latency_interval_width_limit` (`0.40`), because a run that cannot exclude a
regression of three times the ceiling carries no acceptance information. Point
estimates stay recorded and validated, but they are descriptive for the
decision.

`python3 scripts/rust-port/value001_report.py <artifact>` uses the
`ratio_to_percentage` display helper, which multiplies a stored ratio by 100
without modifying the JSON artifact: `0.10` displays as `10.00%`, `3.0` as
`300.00%`, and the retained failed artifact's HTTP ratio
`316.24612017633007` as `31,624.61%`. Timing samples remain in milliseconds and
RSS samples remain in bytes. The report verifies displayed p95 values against
the retained raw arrays, but it never runs a benchmark or changes the gate.
The historical `value001-latest.json` therefore remains `passed: false`; the
migration stays stopped and Go remains in production.

## Implementation progress

- `RUST-007` is in progress. The vault-crate half is now covered: `retention.LoadRules`
  and `retention.DocMetaFromDocument` are ported and replayed against the Go-owned
  `testdata/port/vault/retention-rules.json` (15 rules-file cases and 10
  document-to-metadata cases, every expectation generated by executing Go) through
  `make retention-rules-differential`, which also runs in the native Rust CI job; the
  fixture carries a checksum in the provenance manifest. Merged as #999, with the
  oracle provenance re-frozen on main in #1001; the native `Rust native` CI jobs ran
  `make retention-rules-differential` on Linux, macOS and Windows at `e971824f` and the
  `Rust port contract` jobs verified the fixture checksum. CFG-004 (config filesystem
  writes) is now covered by its own Go-owned fixture and byte-exact Rust replay
  (`testdata/port/config/config-save.json`, 9 cases; `make config-save-differential`),
  merged as #1005 with the oracle provenance re-frozen on the merge revision in #1007;
  the target still has to be wired into the native Rust CI job before the row can
  reach `PASS`. The CLI-level `symdesk retention list` slice is ported and gated by
  `make retention-cli-differential` (`testdata/port/cli/retention-cases.json`, 6
  byte-exact cases) as PR #1008; `retention eval/accept/reject/diff/history` stay open
  because their state is read and mutated through `internal/service`, which is not
  ported yet — the Rust CLI deliberately omits them instead of approximating them.
  DATA-001 (dataset sync) also remains open, so the row stays `TODO`.

- `RUST-001` passed: generated fixtures freeze 207 SymDesk command nodes (206
  non-root, including Cobra's generated help/completion tree), the production-derived SymRoom parser grammar, 57 SymDesk and 8
  SymRoom MCP tools, and 21 HTTP routes. The neutral harness compares both Go
  binaries in isolated sandboxes and its explicit same-binary 26-case Go
  self-test is green. Real differential runs reject identical binaries unless
  that self-test override is passed.
- Production-source provenance covers Go source, embedded assets and migrations,
  the vault contract, and release inputs. A checked provenance commit `Q` must
  contain only listed `testdata/port` derived outputs, must directly follow its
  full recorded source commit `P`, and validates fixture/checker digests from
  immutable Git objects. Fixture checksums and source drift are executable CI
  gates.
- `RUST-002` passed: the Rust 1.98 workspace contains only `symdesk-core`,
  `symdesk-cli`, and `symroom-cli`; 17 Go↔Rust version cases pass byte-for-byte,
  together with format, Clippy, nextest, doctest, feature, coverage, audit, deny,
  and local macOS gates. Linux/Windows native gates are configured in CI; their
  matrix rows remain `TODO` until CI executes them.
- `RUST-003` passed locally: Go-generated fixtures and safe Rust parity are
  green for SimHash, document-format policy, OCR dehyphenation/language hints,
  German FTS/trigram normalization, and the complete search-query/date parser
  (22 query and 17 date cases). Unified configuration parity covers defaults,
  all documented environment overrides (including the four variables from
  [#854](https://github.com/danieljustus/symaira-desktop/issues/854)), ordered
  validation, base XDG/HOME paths, secret-safe state, unknown TOML keys,
  malformed input, and byte-exact Go encoder output. The full `symdesk-core`
  slice passes Miri. Configuration fixtures are regenerated from the Go loader
  and pin non-empty string, non-negative numeric, TOML-precedence, invalid, and
  empty-value behavior. Go remains production.
- `RUST-004` passed: the `symdesk-vault` crate passes 34
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
  `MobileNoteWriter` document is parsed by Swift, Go and Rust. Linux, macOS and
  Windows native CI passed on `main` in run
  [34051534054](https://github.com/danieljustus/symaira-desktop/actions/runs/34051534054).
  `RUST-005` (minimal sidecar index and search) passed. `symdesk-index`
  includes byte-matched migrations 001–011, WAL,
  foreign-key and five-second busy settings, typed file/property/link rows,
  original/German/trigram FTS, snippets/scoping, update/delete semantics and
  Go-compatible partial-batch failure behavior. A Go-generated fixture freezes
  36 schema objects, 11 migration versions, logical database snapshots and nine
  searches. Independent Go/Rust helpers also pass bidirectional database
  create/mutate/reopen, NULL/nanosecond, corruption, read-only, rollback,
  native lock and deterministic 10,000-document full-state/search gates.
  SQL checkout line endings are pinned to LF after reproducing the original
  Windows CRLF hash failure. Linux, macOS and Windows native CI passed on
  `main` in run
  [34102228025](https://github.com/danieljustus/symaira-desktop/actions/runs/34102228025).
  `rusqlite` 0.40.2 is pinned with only `bundled`; the Rust crate has
  zero unsafe expressions, while the reviewed SQLite wrapper/FFI remains an
  explicit transitive boundary.
- Unverified WIP — not evidence: `migration/rust-room-journal` (`f015a694`) holds a
  SymRoom journal draft (Go contract test, fixture, crate change) from a worker whose own
  replay was not byte-exact and whose Make target called the generator instead of
  verifying it; the draft is kept as WIP and was not promoted. The writer worktrees
  `.worktrees/subagent-sa-1-905b9a86` and `../w-rust-room-journal` still hold the two
  differing `testdata/port/room/journal.json` captures.
- Next actions for RUST-007: wire `make config-save-differential` and
  `make retention-cli-differential` into the native Rust CI job so CFG-004 and VAULT-006
  can leave `TODO`, port the `internal/service` retention-state layer that
  `retention eval/accept/reject/diff/history` require, and close #1006 so the new CLI
  cases can compare the filesystem manifest again.

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

## Refreshing port provenance

Port provenance is recorded evidence, not metadata that may be edited beside an
arbitrary change. The recorded revision must be the checked revision or one of
its ancestors — a direct parent/child pair is *not* required, because this
repository squash-merges every pull request and the commit that records an
advance can therefore never be a direct child of the commit whose bytes it
records.

The procedure after any change to production source, `go.mod`/`go.sum`, the
release contract, or the fixture generators:

1. Merge the functional change as **P**; `main` is briefly red on the port
   contract until step 4 lands.
2. On the updated `main`, run `make port-fixtures-generate` from a clean
   worktree. Generation resolves the oracle to `HEAD` by default, and the
   `--oracle-commit` flag accepts an explicit revision only when it is `HEAD`
   or one of its ancestors.
3. Inspect the resulting `testdata/port` diff, then commit the allowlisted
   derived fixture paths and `testdata/port/provenance.json` as **Q**.
4. Run `make port-fixtures-check` at **Q**. The check reads immutable Git blobs,
   validates regular-file tree entries, runs every manifest-covered generator and
   package check from a disposable linked worktree at **Q**, and strips ambient
   generation, Go, Git and PATH overrides. Its Make-side environment prefix cannot
   be replaced by `make PORTGEN_CHECK_ENV=:`.

What the check enforces, fail-closed:

- The recorded oracle revision is the checked revision or an ancestor of it; a
  malformed, unknown, future or side-branch revision is rejected.
- The recorded production digest equals the bytes at the recorded revision *and*
  the bytes of the checked tree, so production source cannot drift and fixtures
  cannot be relabelled.
- Every fixture in the manifest is compared by checksum against the checked tree,
  so a fixture edited after the oracle was recorded is rejected regardless of the
  commit graph.
- The generator digest is bound to the checked tree, not to the oracle revision:
  a harness change is reviewed through the regenerated fixtures instead of
  forcing an oracle advance, which would otherwise be circular.

Changes to the generator Go sources under `scripts/rust-port` and the listed
port-contract test files change the generator digest and therefore require
deliberately regenerated and reviewed fixtures.

## Execution rule

Start at the first `ready` item in `work-items.json`. Complete its fixture,
differential, security, and platform gates; update the matrix and exactly one
work item; then unblock direct successors. Stop on unexplained parity drift or a
failed value gate. Never silently normalize a mismatch.
