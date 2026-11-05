# Go-to-Rust migration record

> **Status:** implementation active; `RUST-001` through `RUST-006` passed; `RUST-007` and `RUST-016` are in progress. VALUE-001 passed for the exact candidate `5c5e98c5`; later commits need their own integrated verification.
> **Go behavior oracle:** the canonical live source P and release are recorded in [provenance.json](../../testdata/port/provenance.json). Every live fixture uses this same identity, backed by the complete production-source digest and immutable ancestry checks. Explicit historical input/probe roles are documented in [fixture-identity.md](fixture-identity.md). VALUE baselines remain pinned to `ae863319` / `v0.12.2`.
> **Scope:** the Go `symdesk` and `symroom` backends; SwiftUI clients and Swift packages stay Swift
> **Tracking:** [#852](https://github.com/danieljustus/symaira-desktop/issues/852)

> **Current entry point (2026-11-02):** [handover-20261102.md](handover-20261102.md) — finish RUST-014/015 and the residual rows locally, remove Go (RUST-019), then push once.

## Cutover decision (2026-10-30)

The maintainer replaced the dual-binary cutover with the EraseMe model. The
stable release ships Rust only: no Go fallback binary, no Go-to-Rust-to-Go
rollback exercise and no seven-day observation window. Go sources are removed
at the cutover. Data safety is kept and becomes a cutover gate instead: Rust
must open existing Go-created state unchanged, and the first Rust run creates
an automatic backup whose restore is verified. Windows is out of scope.
Remaining work is completed locally on the MacBook and pushed once complete.
Native acceptance in this mode is a local macOS arm64 run plus a local Linux
arm64 run in the `symdesk-linux-gate` container. A row whose only residual was
native CI or Windows becomes PASS on that evidence; Linux amd64 is covered by
the single CI run of the final push.

## RUST-018 cutover data safety — Go-created state and the pre-Rust backup

Two gates of the Rust-only cutover decision above, implemented without any Go
fallback. Everything below replays without Go (`make port-contract-frozen`).

**Go-created state opens unchanged** (`make cutover-state-differential`). The
Go recorder `cmd/symdesk/cutover_state_port_test.go` (`make
cutover-state-fixtures-generate`, run once per OS) builds the real Go `symdesk`
and `symroom`, creates a realistic tree in a disposable root at one fixed
absolute path, `/tmp/symdesk-cutover-v1` in its canonical spelling
(`/private/tmp/...` on macOS), and freezes it: sidecar (`sidecar.db`), the
shared and per-vault retrieval indexes, an ingested Markdown file (ingest
store, archive, note), the Go contacts store (two persons, one organization,
created by `internal/contacts/cutover_seed_port_test.go` because the domain
services are internal to that package), saved views and a base note, a signed
SymRoom journal (two identities, owner and a second member, `room.created`,
notes and a decision), plus every `config paths` location. The fixed path is
required because the sidecar and retrieval databases embed absolute vault paths
and the vault-hash directory names. Fixtures: `testdata/port/cutover/state-v1-<os>.json`
(manifest: file modes, sizes and SHA-256, per-state Go outputs, write
scenarios) with `state-v1-<os>-{unmigrated,settled}.cxa` (gzip archives with
modes and nanosecond mtimes; the format is read by `crates/test-support/cutover_state.rs`
without extra dependencies).

Two states are frozen per OS: `unmigrated` (exactly what the Go commands left,
before any read: the first read of either implementation lazily seeds the
per-vault retrieval index from the shared one) and `settled` (after one Go read
pass; Go itself leaves it unchanged). 28 `symdesk` and 6 `symroom` read-only
queries per state (config paths, ls, search, backlinks, props, graph, docs,
views, base notes, ingest jobs, three contact lookups plus a miss, sidecar list,
sources, index status/location, related, room log/verify/members/identities)
are replayed by the Rust binaries and compared byte for byte with the recorded
Go stdout/stderr/exit code (`crates/symdesk-cli/tests/cutover_state.rs`,
`crates/symroom-cli/tests/cutover_state.rs`; absence of the fixture for the host
is a failure, not a skip; a lock file serializes the two test binaries on the
shared root). The tree must then be byte-identical to the Go tree: for the
settled state to the recorded one, for the unmigrated state to what Go's own
first read produced, where the only declared difference is the per-vault
`retrieval.db` (SQLite snapshot bytes differ between implementations; its rows
are compared with the standalone store it was seeded from). Normalized, and
nothing else: `vaults/<hash>/metadata.json` `last_used` (both implementations
rewrite it on every command), SQLite `-shm` files and zero-length `-wal` files,
heap addresses in Go `%v` output and slog timestamps. Rust then performs one
write each (note + index refresh, saved view, ingest, a Rust-signed room
event, `index rebuild`) and a second Rust process must still open every store;
`symroom verify` passes over the Go-signed plus Rust-signed chain.

Incompatibility found and fixed: Go prints an slog line on stderr whenever a
command lazily seeds or migrates the per-vault retrieval index
(`seeded per-vault retrieval index from standalone store`, `migrated
pre-absorption retrieval index to per-vault location`); Rust performed the same
migration silently except under `conflict resolve`. The migration now reports
through a hook in `symdesk-index` (`set_retrieval_migration_hook`) that the CLI
turns into Go's line for every command. The comparison was not weakened.

**Automatic backup and verified restore** (`crates/symdesk-core/src/pre_rust_backup.rs`,
user documentation in [BACKUP.md](../../BACKUP.md)). On the first run that finds
existing state and no marker, `symdesk` copies the data directory (vault-hash
databases, `symingest.db`, `symrelate.db`), the config directory, the legacy
`symingest`/`symrelate`/`symaira-seek` directories and configured database
files to `$XDG_DATA_HOME/symdesk/backups/pre-rust/` through a temporary
directory and a rename; `manifest.json` (`schema_version`, `created_at`,
`symdesk_version`, `sources`, `files[{source, backup, sha256, size, mode,
mtime_ns, method}]`, `dirs`, `skipped`) is written last. `symroom` does the same
for its identity store (`$XDG_DATA_HOME/symroom`). Decisions:

- *Never stdout, one stderr notice.* MCP stdio frames stay clean
  (`pre_rust_backup::mcp_stdout_carries_only_json_rpc_frames_while_the_backup_runs`).
- *Failure policy:* if state exists and the backup fails, the command is
  aborted with exit 1, a clear message and the state untouched (data safety is
  the gate). Escape hatch: `SYMDESK_PRE_RUST_BACKUP=off`, which is also what
  `isolated_env()` and every other test spawn sets so the Go-recorded
  side-effect fixtures keep their exact trees.
- *No state:* nothing is backed up; a marker (`backups/pre-rust.done.json`)
  records `no-state` so state the Rust build itself creates later is not
  mistaken for Go-era state. A backup in progress is guarded by a lock file.
- *SQLite:* quiescent databases (no live WAL/journal, unchanged while copied)
  are copied byte for byte so the restore is byte-identical; anything else is
  snapshotted with `VACUUM INTO` (WAL-consistent, integrity-checked) and its
  `-wal`/`-journal` are folded in rather than copied.
- *Skipped:* `archive/` and `backups/` at the top of a state root, `-shm`.
- *Restore* is the hidden `symdesk|symroom backup restore-pre-rust [--dry-run]`.
  It is intercepted before Cobra resolution, so the Go-recorded command tree,
  `--help` and completion fixtures stay byte-identical (the Cobra model does
  support `Hidden`, but a Rust-only node would change the 207-node inventory
  contract). It verifies every digest, moves the current state aside to
  `backups/pre-restore-<time>-<pid>/` (never deletes), restores files through
  temporary files and renames with modes and mtimes, re-verifies, and moves the
  previous state back if anything fails.

The restore exercise on Go-created state (both frozen states, both OSes): first
Rust run → backup with the Go digests → `sources add`, `ingest` and `index`
change the stores → dry run → restore → the data/config locations are
byte-identical to the Go tree, and with the Rust-added vault entries removed
(the vault is user data and is not restored) the whole tree is → all 28 queries
match the recorded Go output again. The same for the SymRoom identity store
(Rust-created identity set aside, journal still verifies).

Audit points (previous worker could not confirm them; each is now pinned by a
test):

- *Case counts.* 34 Go-recorded queries per state (28 `symdesk` + 6 `symroom`)
  × 2 states (`unmigrated`, `settled`) = 68 per platform, identical on macOS
  arm64 and Linux arm64 (`state-v1-darwin.json`, `state-v1-linux.json` each
  hold 34 queries and 4 write scenarios per state, read out of the committed
  manifests; the fixed root is `/private/tmp/symdesk-cutover-v1` on macOS and
  `/tmp/symdesk-cutover-v1` on Linux). The frozen
  counts are `cargo test -p symdesk-cli --test cutover_state` 4,
  `--test pre_rust_backup` 11 and `cargo test -p symroom-cli --test
  cutover_state` 3.
- *Incompatibility fixed.* The only behavioural divergence the Go state
  exposed was the missing slog line for the lazy retrieval-index migration
  (above); the replay compares stderr, so the divergence failed the test
  before the fix.
- *Abort on failure.* `failed_backup_aborts_the_command_and_leaves_state_untouched`
  puts a regular file where `backups/` must go: exit 1, empty stdout, the
  message names `SYMDESK_PRE_RUST_BACKUP=off`, the whole tree is byte-identical,
  and the opt-out then lets the command run.
- *Hidden restore command and help parity.* `backup_cli::intercept` matches
  exactly `<bin> backup restore-pre-rust ...` before Cobra; nothing is added to
  the Cobra tree, usage text or completion. `every_command_help_and_completion_match_go_process`
  (424 help-process cases against the recorded Go bytes) and the 207-node tree
  fixtures pass unchanged; `restore_command_is_hidden_from_help_and_other_backup_words_stay_unknown`
  pins that `symdesk backup` is still Go's `unknown command "backup"`.
- *The gate sits behind the Cobra front half and skips `version`.* Help,
  `--version`, completion, `__complete`, unknown commands and flag errors are
  answered by Cobra before the gate and therefore never create a backup or add
  a stderr line; `symdesk version` (valid, but reads no state) is exempted
  explicitly (`cobra::resolved_key`), so the Go bytes of `version` hold even on
  the first run with existing state and the Homebrew `test do` / release smoke
  see no notice. `symroom version` is exempted the same way. Pinned by
  `help_version_completion_and_usage_errors_never_trigger_or_change_the_backup`
  (found while auditing: before this change `symdesk version` ran the backup).
  Anything the gate cannot classify (non-UTF-8 argv) fails safe: it backs up.
- *Restore exercise.* Both frozen states on both OSes, see above; the Rust
  writes in between are real (`sources add`, `ingest`, `index`).

Declared limits: the restore moves the current state aside with `rename`, so
the config directory and `backups/` must be on one filesystem (a cross-device
rename fails, and the failure path puts everything back); the vault, rooms and the ingest `archive/` are not backed up
by the tool (the vault is the user's; see BACKUP.md); a retrieval index
relocated by a custom `index_path` outside the state roots is derived data and
is not copied; the first run on a very large data directory copies it all;
Windows and macOS Intel are out of scope.

**Verification at `58e0933b`** (the Rust sources are identical to `53cef398`;
only the generator digest in `testdata/port/provenance.json` and the
recorded `pre_rust_backup` count differ; later commits are docs-only).
Receipts: `local-gate-r018/receipts.jsonl` next to the build directory
(`builds/symaira-desktop/.worktrees/r018-backup/`), `LOCAL_GATE_SLUG=r018`.

| Gate | Host | Result |
|---|---|---|
| `rust-check`, `rust-lint` | macOS arm64 | exit 0 (`macos-53cef398-152059.log`, `-152124.log`; `cargo fmt --check` of main's `symdesk-protocol` import order fixed in this branch) |
| `rust-check`, `rust-lint` | Linux arm64 | exit 0 (`linux-58e0933b-183451.log`, `-185053.log`) |
| `rust-test` | Linux arm64 | exit 0, 1026 passed, 5 skipped (`linux-58e0933b-195026.log`) |
| `port-contract` | Linux arm64 | exit 0 (`linux-58e0933b-190630.log`), includes `cutover-state-differential` against the Linux-recorded state |
| `cutover-state-differential` | macOS arm64 | exit 0 (`macos-58e0933b-211009.log`): Go re-check, then `cutover_state` 4, `pre_rust_backup` 11, symroom `cutover_state` 3 |
| `port-contract` remainder (`sidecar-metadata`, `room-journal`, `room-run-projection`, `dataset-sync`, `retention-state`, `retrieval-turboquant`) | macOS arm64 | exit 0 (`macos-58e0933b-210906.log`) |
| `port-contract` as one target | macOS arm64 | **not green**: `port-fixtures-check` and `cutover-state-differential` pass, then `sidecar-differential` stops on `symdesk-index source_tests::external_source_watch_syncs_nested_create_modify_delete_and_stops` (FSEvents; fails 0/15 in isolation on this host, see below). Earlier attempts failed `port-fixtures-check` itself on `TestPortCLIAdminBodiesProcessContract` / `TestPortComposeProbeContract` (fake-tool version probes time out under load: `version unknown` instead of `1.2.3`, load 20-100) |
| `rust-test` | macOS arm64 | 1025 tests; at `53cef398` (`-152150.log`) all pass except `http_surface rust_server_replays_the_go_http_surface` ("plaintext request on the tls port", load 30); at `58e0933b` (`-162509.log`, `-174135.log`) only host-timing tests fail |
| `port-contract-frozen` | both | exit 1 on both hosts and on main `e8b7e654` itself: `test_recorded_manifest_covers_the_real_makefile` lists 46 entries (ingest, contacts, render, retrieval, MCP and other differential replays) without recorded counts; none are RUST-018 entries. The three RUST-018 replays are recorded and pass Go-free (PATH without `go`, isolated HOME): 4 / 11 / 3. Not recorded here: another worktree (`r-frozen`) is recording the 46 |

macOS host-timing failures, honest account. `fseventsd` has used ~100% CPU on
this host for days and the load averaged 10-100 from concurrent agents:

- `symdesk-cli::http_surface` (TLS plaintext close) and `cli_admin_bodies` /
  `TestPortCLIAdminBodiesProcessContract` (version probe) failed under load but
  **passed at load < 8** on the final tree (`retest2.log`: 43 s and 49 s), and
  `sources_match_go_process_fixture` and `symdesk-ingest watcher_contracts` also
  passed there.
- `symdesk-cli::signals signals_match_go` (`events_first_vault_change`,
  `events_idle_sigterm_exits_zero`, `sources_watch...`), `symdesk-index
  source_tests::external_source_watch_*` and the two `symdesk-protocol
  snapshot_cache` watcher tests passed on the first `53cef398` run (signals and
  source watch on retry 2 and 3 of 5, the two snapshot-cache tests first time)
  and failed in the later attempts (nextest
  retries 5/5 and the single-test loop, 15 of 15 for the source watch, also with
  `TMPDIR` on the internal disk). They exercise FSEvents only, none of the files
  changed by RUST-018 are on their path, and they pass on Linux (inotify).
  **They have not passed on the final tree.** They need a host with a healthy
  `fseventsd` (restart needs root) and a re-run before merge.

**DIST-006 / DIST-007 re-scoped (never silently).** The matrix rows were
written for the dual-binary model: DIST-006 asked a frozen Go fallback to
reopen Rust-written data, DIST-007 asked for `symdesk-go`/`symroom-go` archive
members and Homebrew/Docker/HA fallback selection. The cutover decision of
2026-10-30 (above) drops the Go fallback. DIST-006 therefore becomes the
evidence of this section (pre-Rust backup + restore exercise, rows
CUTOVER-001/002) and DIST-007 is **RETIRED** because no Go fallback binaries
ship; the archive-member half that survives ("exactly `symdesk` and `symroom`")
is already DIST-001/002. `rollback-harness.md` (Go reopens a Rust-written
SymRoom journal) remains a local regression check and is not required.

## Local completion checkpoint — 2026-10-31

Local `main` 8c7cee5f (not pushed). Passed (local): RUST-001…009, RUST-011,
RUST-016. In progress: RUST-010 (SEC-001 complete; INGEST-001…006, HTTP-004
open) and RUST-012 (AI-001/003 PASS (local); AI-002 open). 46 matrix rows are
PASS or PASS (local). Two-host gate at 8c7cee5f exit 0 on macOS arm64 and
Linux arm64: rust-check rust-lint rust-test port-contract
render-ir-differential render-differential retrieval-differential
sources-cli-differential contacts-dataset-differential
dataset-aggregate-differential ingest-differential ai-compose-differential
retrieval-ask-offline-differential. Next: RUST-013 after RUST-010/012.

## Local completion checkpoint — 2026-10-30

Local `main` a586b0f0 (not pushed). RUST-016 passed; RENDER-001 and the
SymDraw PNG slice (RENDER-003) are PASS (local); 35 matrix rows are PASS or
PASS (local). Two-host local gate receipts at a586b0f0: `port-contract`,
`render-ir-differential`, `render-differential` exit 0 on macOS arm64 and
Linux arm64; `rust-check rust-lint rust-test` exit 0 at 893d442b (Linux) and
`rust-test` at 2912360a (macOS) after the timed SymRoom MCP replay was made
exclusive in `.config/nextest.toml`. Open: RUST-008, RUST-011 (RENDER-002 and
the rest of RENDER-003), then RUST-009/010/012 and everything behind them.
`verify.py` needs a fresh `RENDER_EVIDENCE_ROOT` per run.

## Local integration checkpoint — 2026-10-26

Execution mode: local-first on branch `rust/integration` (worktree
`Symaira_Dev/Worktrees/symaira-desktop/rust-int`), one PR per work package,
CI only on those PRs. Scope authorized: RUST-008 through RUST-016; no release,
cutover or Go removal.

- Integrated night PRs #1346–#1348 (closed with reference) plus three worker
  waves: retrieval document extraction (60 Go cases, re-embed for all formats),
  SymDraw lowering/SVG/charts/glyph outlines (7,344 emit cases), contacts
  schema/CSV/vCard/import/CRUD/relationships/erase/key-provider, ingest queue,
  extraction (84 cases), Notion and Paperless importers, AI providers and
  compose resolution/probes (90 cases, loopback only), CLI help/completion for
  all 207 paths, config/ai-config, docs list/review, views reads, backlinks,
  inverse relations, MCP catalog for all 57 tools and 16 call handlers.
- Verified at `ee0bb584` on Darwin/arm64: `cargo fmt --check`, strict
  workspace Clippy, `cargo test --workspace --all-features` (729 passed, 3
  ignored), `CGO_ENABLED=0 go test ./...` (130 packages ok) with go1.26.10,
  `portgen --check` PASS. Native Linux/arm64 replay runs in Docker
  (`symdesk-linux-gate:3`: Debian trixie, git 2.47, go1.26.10, rustc 1.98.0);
  the SymRoom set (32 differentials, 68 Rust tests) passed at `5c9c54ae`.
- Integration lessons: test with `--workspace` (symroom-core enables
  `serde_json/preserve_order`, which changes key order everywhere); fixtures
  must stay under `testdata/port/`; generate targets must use
  `$(PORTGEN_GENERATE_GO_ENV)`; filenames whose acceptance differs between
  APFS and ext4 need per-GOOS fixture entries
  (`testdata/port/ingest/paperless-export-unicode.json`).
- Open: every work package still has residual rows (see work-items notes and
  worker ledger notes); native Windows evidence for all new slices is pending
  the first package PR CI run. Go remains the production implementation.

## Local handoff — 2026-09-24

The native portability repairs were subsequently squash-merged as functional
P `36b30c48` in [#1157](https://github.com/danieljustus/symaira-desktop/pull/1157).
The source/watch and note-operation guards now name that actual main revision;
the P/Q refresh regenerates their evidence and the full fixture provenance from
P rather than preserving side-branch identities. Local Go race, Rust workspace,
strict Clippy and immutable fixture replay passed before merge. Actual Windows
Go Search/Ask and metadata-sharing controls passed three times each, followed
by the complete all-feature Rust workspace in
[run 37118990189](https://github.com/danieljustus/symaira-desktop/actions/runs/37118990189).
The six-platform native workflow remains pending; this does not promote any
migration work item or certify the outstanding macOS alias-root acceptance.

- Integration source and Go-owned fixtures: `bb47a830` on
  `codex/rust-migration-integration`. `history tasks` now replays six Go process
  cases, including empty JSON `null`, ordering, text output and partial status.
- At that revision, `go test ./...` (CGO disabled), `make lint`,
  `cargo test --workspace --locked`, strict all-feature/all-target Clippy,
  `make history-tasks-cli-differential` and `portgen --check` passed locally on
  macOS. The Rust suite contains explicitly ignored tests; these passes do not
  replace their separate gates.
- This is a local handoff, not migration acceptance: `contract-matrix.md` still
  has TODO rows and the exact integrated Linux/Windows native evidence is
  absent. Go remains the production implementation. No push, publication,
  cutover or Go removal occurred.

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
8. macOS arm64/amd64, Linux arm64/amd64, and Windows arm64/amd64 remain supported unless a separate compatibility decision removes a target. Intel macOS (amd64) native evidence is produced by the nightly `schedule` and manual `workflow_dispatch` runs of CI, not by every push to `main`; the acceptance evidence for a SHA is the latest scheduled or dispatch run on that SHA.
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
The historical `value001-latest.json` remains `passed: false`; the exact
`5c5e98c5` candidate was accepted separately. Go remains in production.

## Implementation progress

- 2026-10-03: repair the migration CI failures seen at `e817394e` without
  advancing migration acceptance. The external-source live differential keeps
  its immutable source guard and anchors the reviewed functional Go source
  recorded by P/Q (including #1129). Unix-only search-test imports are Unix-gated,
  offline Ask fixture projection recognizes native Windows source paths, and
  exact-version ISC and CDLA-Permissive-2.0 exceptions cover the existing
  Rustls crypto dependencies and Mozilla CA bundles.
  Native CI additionally exposed a Go notebook match/fallback identity bug on
  Windows: matched relative paths now use the same slash notation as notebook
  source references, preventing duplicate fallback citations. The existing
  service regression now requires exactly one result per notebook source.
  Unscoped Go and Rust searches now emit slash-form vault-relative references
  too, keeping nested citations identical to notebook references on Windows.
  Rust's previous deliberate native-path fallback duplication is updated with
  the Go fix. Native Windows CI confirmed the remaining backslash citation
  mismatch at `65a331c8`; the paired repair passes affected Go service tests and
  all 79 protocol tests on Linux. MCP Search projects decoded path fields before
  JSON encoding, preserving result field order and non-path values.
  The isolated Rust Search/Ask processes preserve Windows `SYSTEMROOT`, as the
  MCP Search harness already does, so the native socket runtime can initialize.
  The MCP negated-path corpus now targets `private.md`, rather than the broad
  substring `private` that also excludes macOS's canonical `/private/var` temp
  root. Go generation under a temp path containing `private` confirms that
  only the query input changes; its expected results and requests are preserved.
  These reviewed input/source changes require a fresh P/Q record. Squash merge
  requires the subsequent Q refresh to anchor the actual merged P commit.
  Linux/amd64: source registry/search and recursive watch differentials, the
  race-enabled Go offline Ask oracle, strict workspace Clippy and the native
  sidecar round-trip pass. Other native runner results are required separately.
- #1127: the existing CLI alias-root regression is now complemented by real
  MCP Search and offline Ask process replays through symlinked vault roots.
  Both canonical and alias cases use the Go-owned expected envelopes
  and provider request traces; no fixture expectations are normalized away.
  These four MCP process tests pass on Linux/amd64. The full Rust workspace
  passes locally with the fixture-generating `umask 022`; the managed cloud
  default `077` produces different expected directory permissions, so it is
  not interchangeable with the recorded POSIX-mode fixture environment.
  Go's CLI Search, MCP Search, MCP Ask and service Ask oracles also pass with
  an explicitly symlinked `TMPDIR`: their test-owned storage roots are resolved
  before database construction and the Search harnesses use isolated canonical
  XDG data roots. SQLite's no-symlink storage checks are preserved.
  The Go race suite's Linux timeout negative control now distinguishes an
  already-exited zombie from a live descendant when container PID 1 does not
  reap orphans; the process-group kill requirement remains enforced. The full
  local Go race run passes at `8cb31b23`; the subsequent Windows notebook fix
  passes the affected service tests locally. PDF missing-tool controls use an
  empty test-owned PATH. Exact integrated native acceptance is still pending.
- #864: `baseline-20260906.json` now explicitly records the historical index
  and graph benchmarks' actual 10,015 documents and retains their original 10,000 claims
  as `reported_documents`; the timing is unchanged. The independent
  `testdata/port/sidecar/large-corpus.json` supplies exactly 10,000 documents,
  fixed mtimes, a logical-state hash (volatile database timestamps excluded)
  and ordered search expectations. `make sidecar-roundtrip` verifies counts,
  hashes, searches and bidirectional Go/Rust reopen on Linux/amd64.

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
  the target was wired into the native Rust CI job in #1012 and verified on
  Linux/macOS/Windows at `bdb017c7` (Rust native matrix run 35642921769), so the row
  can now reach `PASS`. The CLI-level `symdesk retention list` slice is ported and gated
  by `make retention-cli-differential` (`testdata/port/cli/retention-cases.json`, 6
  byte-exact cases) as #1008, verified on Linux/macOS/Windows in the same native matrix;
  The current integration branch adds local Go↔Rust differential evidence for
  `retention eval/accept/reject/diff/history`, dataset sync/import/purge,
  explicit history/trash purge, and history policy pruning with checkpoint
  protection and blob collection. Native evidence at the exact integrated
  commit remains open, so VAULT-006 and DATA-001 stay `TODO`.

- `RUST-016` is in progress. Local Go↔Rust gates cover signed identity/events,
  journal basics, run projection and CLI transitions, the `note` CLI command,
  journal merge reads, MCP framing and all eight declared tools, including
  signed mutation effects.
  The Room event fuzzer has a valid seed and passed 1,000 local runs. Remaining
  CLI families, journal/index behavior and exact native platform evidence keep
  the item open.

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
- Superseded WIP — not evidence: `migration/rust-room-journal` (`f015a694`) held a
  SymRoom journal draft from a worker whose own replay was not byte-exact and whose
  Make target called the generator instead of verifying it. It was never promoted;
  the journal landed instead through `migration/rust-room-journal-v2`
  ([#1017](https://github.com/danieljustus/symaira-desktop/pull/1017)). The draft
  branch and its writer worktrees no longer exist.
- Done since `b98ca146`: the CI wiring landed in #1012, CFG-004 reached `PASS`, and
  #1006 is closed on `migration/rust-sidecar-metadata` — `open_for_vault` now writes
  `vaults/<hash>/metadata.json` byte-identically to Go (13 recorded encoding cases —
  Go's HTML escaping of `<`/`>`/`&`, U+2028/U+2029 and its trimmed nanosecond layout —
  plus 3 directory-listing cases, the 0700/0600 POSIX modes and the
  open/reopen/explicit-override filesystem blocks), `retention list`
  no longer closes the sidecar Go leaves open, and the sidecar layout gate requires
  `vaults/<hash>/{sidecar.db,sidecar.db-wal,sidecar.db-shm,metadata.json}` on both sides.
  Full `compare_files` deliberately stays off: `sidecar.db` carries SQLite state and
  `metadata.json` a timestamp, so neither can match byte-for-byte across two processes —
  the layout gate plus the byte-exact `metadata.json` replay are the filesystem evidence.
- Next actions for RUST-007: re-freeze the integrated fixture provenance,
  then execute all applicable gates on the
  required native platforms. The retention CLI keeps Go's open sidecar handle
  behavior across `list/reject/diff/history`.
- Skills loaded this session: `go-to-rust-migration` + `references/worker-dispatch.md`.
- Live evidence runs (manual `workflow_dispatch`, because `Rust port contract`
  and `Rust native` are `if: event_name != 'pull_request'`): run `35735140018`
  on `migration/rust-sidecar-metadata` and run `35735143364` on
  `migration/rust-room-journal-v2` — both `conclusion=success` with every job
  green, including `Rust port contract` and `Rust native` on Linux, macOS and
  Windows. These supersede run `35731356950`, which failed on
  `fields 'directory_mode' and 'file_mode' are never read` (the fields were
  read only under `#[cfg(unix)]`, so `clippy -D warnings` rejected them on
  Windows alone) plus one flaky `iOS app` test, now filed as
  [#1019](https://github.com/danieljustus/symaira-desktop/issues/1019).
  PR: [#1016](https://github.com/danieljustus/symaira-desktop/pull/1016).
  Known pre-existing flake: `internal/compose` `TestInspectToolReportsManagedAndPATHCopies`
  under parallel `-race` load, filed as
  [#1018](https://github.com/danieljustus/symaira-desktop/issues/1018).
  Note: a second desktop chat worked the same worktrees on 2026-09-22 (commits
  `0a93593f`, `8c449e7d`, `c5cb9a0a`); `8c449e7d` deliberately keeps the sidecar
  metadata fixture on the room pattern — outside the portgen manifest, gated only by
  `make sidecar-metadata-differential`.

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
2. On the updated `main`, run `make port-fixtures-generate PORT_ORACLE_COMMIT=$(git rev-parse HEAD) > q.patch` from a
   clean worktree (no untracked or ignored Go inputs under `cmd`, `internal`,
   `scripts/rust-port` or `vendor`). Generation runs in a disposable worktree,
   uses the central provenance P by default (the command above selects the newly merged P), and writes a
   `# SYMAIRA PORT FIXTURE PATCH V1` artifact to stdout; it never touches the
   caller's worktree. The `--oracle-commit` flag accepts an explicit revision
   only when it is `HEAD` or one of its ancestors and its full production bytes match the generating tree. The compatibility `--fixture-oracle-commit` flag must select that same P.
3. Inspect the artifact, which is limited to the allowlisted derived fixture
   paths and `testdata/port/provenance.json`. Either validate it with
   `make port-fixtures-apply PORT_FIXTURES_ARTIFACT=q.patch` (builds and checks
   **Q** in a private worktree) or apply it with `git apply --index q.patch`,
   then commit it as **Q**.
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
deliberately regenerated and reviewed fixtures. The digest covers more than the
harness sources: `go.mod`, `go.sum`, `Makefile` and `.gitattributes` are digest
inputs too (`scripts/rust-port/inventory/digest.go`), so a change as small as a
new Make target needs its own **Q** re-freeze — a functional `P` that adds a
target leaves `main` red on the port contract until that **Q** lands.

## Go-independent frozen contract (#1153)

`make port-contract-frozen` is the contract aggregate that must keep protecting
the Rust port after Go is deleted. It needs only Python 3 and Cargo:

- `scripts/rust-port/frozen_contract.py` reads every `*-differential` target in
  the `Makefile` and runs each distinct `$(CARGO) test` replay of the committed
  Go-oracle fixtures. Every `go` executable is removed from `PATH`, and `HOME`,
  `XDG_*` and `TMPDIR` point into a disposable root.
- `scripts/rust-port/frozen-contract.json` records the exact passed and ignored
  counts for each replay. A count drift, a zero-case run, a failed test binary or
  a missing count fails. New differential targets fail until their counts are
  recorded with `python3 scripts/rust-port/frozen_contract.py --record`, which
  also runs without Go.
- Targets without a Rust replay must be declared `live_only` with a reason, or
  `retired` (reason required, and the target must no longer exist in the
  `Makefile`). A pure aggregate (prerequisites, no recipe line) is classified
  through its prerequisites. **No target is live-only any more** and no
  `live_only_tests` entry remains. The former live-only targets became Go-recorded
  replays: `mcp-differential` and `mcp-initialize-differential`
  (`mcpdiff --record`, `mcp_representative`, 23 raw-process cases of which 7 are
  MCP-001), `index-status-cli-differential` (`index-status/frozen.py`, 126 cases from
  the Go oracle P) and `render-pdfops-hostile-differential`
  (`hostile_replay.py --record`, 455 hostile PDF cases through the real
  Poppler/qpdf, one recording per OS because the tool versions differ).
  `http-differential` is **retired**: see "Retired live HTTP comparison" below.
- Mutation controls change one Go-recorded observation, such as a fixture byte
  covered by the provenance checksum or a recorded journal value. The runner
  requires the replay to fail and then restores the bytes. It refuses to mutate
  a modified fixture.

The disposable root is created under `TMPDIR`. Room and index replays reject
roots with a group- or world-writable ancestor. On such a host, such as the
external Dev volume, point `TMPDIR` at a private directory under `/private/tmp`.

The CI job `Rust frozen contract (no Go)` deletes any preinstalled Go before it
runs the target. The existing live targets (`port-contract` and the
`*-differential` recipes) are unchanged where they only check a fixture against
Go: they still verify the recordings below against the real Go oracle while Go
remains in the tree, and are dropped with Go at RUST-019.

### Go recordings of the formerly live gates (RUST-019 freeze)

Each gate below used to start the Go binary next to the Rust one. The real Go
oracle now writes what it observed once; a Rust test replays the same inputs
and compares. `go run`/`python3` `--check` (also registered in
`testdata/port/provenance.json` and `portgen`) proves a recording still equals a
fresh Go run, so Go-equals-recording and Rust-equals-recording together keep
the former Go-equals-Rust comparison.

| Gate | Recording (cases) | Recorder (real Go) | Rust replay |
|---|---|---|---|
| `representative-differential` (CLI part) | `testdata/port/representative/cli-recording.json` (16) | `cmd/diffrecord` | `symdesk-cli/tests/frozen_diffharness.rs` |
| `dataset-cli-differential` | `testdata/port/dataset/cli-recording.json` (16) | `cmd/diffrecord` | same |
| `retention-cli-differential` | `testdata/port/cli/retention-recording.json` (61) | `cmd/diffrecord` | same |
| `history-differential` | `testdata/port/vault/history-oracle.json` (56 operations) | `cmd/historygen` | `symdesk-vault/tests/history_contracts.rs` |
| `source-differential` | `testdata/port/cli/source-registry.json` (12 steps, 3 external-search hits, registry file) | `source_record.py` | `symdesk-cli/tests/sources_frozen.rs` |
| `source-watch-differential` | `testdata/port/cli/source-watch.json` (ready line, create/update/delete hashes) | `source_record.py` | same |
| `render-pdfops-differential` | `testdata/port/render/pdfops-recording.json` (157 PDF adapter, 13 press, 11 safety) | `pdfops/record.py` | `symdesk-render/tests/pdfops_frozen.rs` (run with `--examples`) |
| `store_paths` (was `live_only_tests`) | `testdata/port/storage/store-paths-{darwin,linux}.json` (20 layouts each) | `store_paths_record.py` (macOS arm64, and Linux arm64 in `symdesk-local-gate:1`) | `symdesk-core/tests/store_paths.rs` (selected by `cfg(target_os)`) |

Harness rules shared by the replays:

- **diffharness.** `diffrecord` builds the Go `symdesk` with
  `-X main.version=devel` (the Rust test binary reports `devel`) and runs each
  case through `internal/diff.Run`; `internal/diff.Observe` applies the very
  normalization `Compare` applies (`normalizeStream`: console text, retention
  run IDs, sidecar layout), so a recording is exactly what a live comparison
  compared. The input case is embedded, so the file is self-contained. The Rust
  side (`tests/support/diff_replay.rs`) rebuilds the sandbox (HOME/XDG/tmp
  roots, setup files with modes and mtimes, reserved-environment rules,
  `prepare_args`, cwd, stdin) and rejects what it does not replay
  (`compare_files` manifests, symlink setup, raw base64 environment): none of
  the 93 cases uses them. macOS arm64 and Linux arm64 Go recordings were
  compared and are byte-identical, so one file serves both.
- **history.** `historygen --check` compares the committed report with a fresh
  Go run; `SYMDESK_HISTORY_ORACLE` still overrides the recorded file for the
  live `history_live.py` evidence.
- **sources.** Variable text is normalized on both sides: canonical and literal
  source/vault roots become `<SOURCE>`/`<VAULT>` and the path-derived source ID
  `folder-<sha256>` becomes `<ID>`.
- **PDF adapters.** The controlled `pdf_tool_stub` example is an input shared by
  both languages, never an oracle. Worlds are rebuilt per case; the comparison
  covers response, tool trace and the before/after file-system snapshot (type,
  mode, SHA-256), plus the lifetime checks (no leaked staging file, no owned
  process left alive).
- **store paths.** The recording carries the native capture root; the replay
  rewrites it (and the hex-encoded symlink targets) to a fresh canonical private
  root and recreates the recorded seed state before it runs the same
  assertions.
- Regenerate with `make <area>-fixtures-generate` (`dataset-cli`, `retention-cli`,
  `representative-cli`, `history`, `source`, `store-paths`, `render-pdfops`) and
  re-freeze the generator digest as before.

## Execution rule

Start at the first `ready` item in `work-items.json`. Complete its fixture,
differential, security, and platform gates; update the matrix and exactly one
work item; then unblock direct successors. Stop on unexplained parity drift or a
failed value gate. Never silently normalize a mismatch.

## Retired live HTTP comparison (`http-differential`)

`httpdiff` compared a Go and a Rust `symdesk serve` live over the 291 requests of
`testdata/port/http/representative.json`. It cannot run without a Go binary and
its stateful per-case vault setup, loopback fakes and persisted-file assertions
exist only in that Go harness, so it was not converted to a recording. The Make
target and the HTTP half of `representative-differential` are removed; the tool
stays for the VALUE-001/002 contract preconditions and the Windows CI job.

Frozen replacement (runs in `port-contract-frozen`): `http-surface-differential`
(`http_surface.rs`, the Go-recorded 84 cases / 1135 steps from the real
`symdesk serve`) and the CLI half of `representative-differential`.

Not frozen, and therefore no longer compared with Go: the 58 `POST /api/v1/ingest`
multipart variants together with the persisted upload and job bytes and modes,
the worker lease/complete/fail and job-retry state machines, and the share-access,
hybrid-index ask and provider-failure paths of that corpus. HTTP-004 keeps its
recorded local evidence (291 transcripts green at `3b95db4d`/`b13bf038`) and the
Rust unit and protocol tests (`symdesk_ingest::gform`, `symdesk-protocol`). A
future recording would have to port the `populate*` setup of `httpdiff` into the
surface generator (`cmd/symdesk/http_surface_*_port_test.go`).

