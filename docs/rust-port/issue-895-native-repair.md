# Issue #895: native migration failure checkpoint

This is a bounded local checkpoint, not native Windows acceptance or RUST-006
completion. Go remains production.

## Starting snapshot

Both `pwd -P` and `git rev-parse --show-toplevel` returned
`<assigned-worktree>`.
Branch: `migration/rust-watch-ci-native-repair-20260910`.
Starting HEAD: `2176334fa6b0b4667b48460728eeb4d7d684beb6`.
`git -c core.fsmonitor=false status --porcelain=v1` returned no changes.

Read `AGENTS.md`, issue #895 acceptance, `implementation-plan.md`, and
`work-items.json`. The issue's reported failures came from run
<https://github.com/danieljustus/symaira-desktop/actions/runs/34266283970>
at `2544e0630e025aede87527e5afb959714a8f7606`, not this starting snapshot.

The starting snapshot already uses explicit Bash with `set -euo pipefail`
for all five affected migration steps, expects `folder/literal/name.md` on
Windows, and pins oracle text inputs to LF through `.gitattributes`.

## Executable changes

- `.github/workflows/ci.yml`: also run the failure/checkout controls before the
  native Windows frozen-oracle step. The Rust native job retains its controls
  and every existing formatting, lint, test, and differential gate.
- `scripts/rust-port/test_native_ci_contract.py`: execute each actual workflow
  body in a temporary directory with test-owned command substitutes that invoke
  the native Python executable. Exercise five successful bodies, then fail
  each of their 23 native commands individually with exit 23. Assert the exact
  executed command prefix, exit status, and absence of later success. Negative
  controls expose masking without `errexit` and with `|| true`; a pipeline
  control verifies `pipefail`. Stub scripts explicitly use LF on Windows.
- `crates/symdesk-protocol/src/lib.rs`: expand the existing platform-specific
  snapshot-path regression to seven cases, covering component preservation,
  repeated and mixed separators, dot segments, trailing separators, and empty
  input. Pinned Go 1.26.6 `src/path/filepath/path.go:89` delegates to
  `src/internal/filepathlite/path.go:176`: replace each native separator;
  preserve repeated separators and do not clean the path. Unix backslashes
  remain literal filename characters.

The command substitutes are deliberate failure controls, not parity fixtures.
No production code, generator Go source, provenance validation, fixtures,
dependencies, thresholds, or shared operation ledgers were changed. No real
vaults or credentials were accessed. Diagnostics remain in these notes and
local logs; no MCP stdout path was modified.

## Focused verification before the local commit

Host: macOS. The coordinator verified the external build volume and used an
isolated Cargo target directory for the focused runs. The Go build cache stayed
internal; Go temporary storage used the external build volume.

| Command | Executed result |
| --- | --- |
| `GOTOOLCHAIN=go1.26.6 CGO_ENABLED=0 go test -count=1 -v ./scripts/rust-port/cmd/portgen ./scripts/rust-port/inventory` | 5 passed: 4 portgen, 1 inventory; run once on the clean snapshot |
| `cargo test -p symdesk-protocol --all-features --locked` | 34 passed, 0 failed, 0 ignored; 0 doctests; run once before and once after changes |
| `python3 scripts/rust-port/test_native_ci_contract.py -v` | Baseline: 2 passed. After changes: 3 passed on each of two runs; each expanded run includes 23 injected command failures, 5 successful bodies, 2 masking controls, and 1 pipeline failure control |
| `cargo fmt --all --check` | Passed after `cargo fmt --all` corrected the new assertion's wrapping |
| `cargo clippy -p symdesk-protocol --all-targets --all-features --locked -- -D warnings` | Passed |
| `GOTOOLCHAIN=go1.26.6 CGO_ENABLED=0 go vet ./scripts/rust-port/cmd/portgen ./scripts/rust-port/inventory` | Passed; no Go files changed, so no Go formatting edits |
| `ruff check scripts/rust-port/test_native_ci_contract.py` | Passed |
| `actionlint .github/workflows/ci.yml` | Passed |
| `git -c core.fsmonitor=false diff --check` | Passed |

These runs executed 81 top-level test invocations in total: 68 Rust, 5 Go,
and 8 Python. Repeated runs are not additional unique coverage. The final
focused coverage is 42 tests, with the unchanged Go packages' baseline result
retained. Injected failures are successful control assertions, not test-suite
failures.

Local raw logs were retained outside the repository for the coordinator's
verification; they are not release or fixture inputs.

## Provenance blocker and remaining acceptance

The reported provenance mismatch is **not reproducible on this exact clean
snapshot**. The real `TestProvenanceVerificationPasses` passed; it validates
working-tree production bytes, pinned Git revision bytes, generator bytes,
and fixture checksums. Existing deliberate fixture and embedded-SQL mutation
controls also passed. The real Git `core.autocrlf=true` checkout control passed
with the existing attributes and the recorded production digest.

Read-only inspection additionally found all 460 production inputs equal to
pinned oracle `745c08e8144971c61133c5d0e5d61c7ce405aad2`; the production digest
is `ac77a66c71a0c8a5428bcfece6556c73f486ec855b75e0ab4e32fafd291db877`.
All 51 Go generator inputs and 27 fixture checksums match provenance.
Every production text input and generator Go input has LF preservation; the
two production files without an LF rule are binary TTF fonts.

No provenance fix or fixture regeneration is justified by this snapshot.
The historical native mismatch remains an explicit blocker until raw native
Windows evidence at the corrected commit explains or clears it. Local Git
checkout-filter simulation and macOS tests do not substitute for Windows.
The native Windows suite and representative CLI/HTTP/MCP differential steps
were not rerun here. The coordinator must obtain and inspect those raw logs
at the checkpoint commit before updating shared RUST-006 evidence.

Initial environment diagnostics: Brain memory retrieval failed; the first
storage status check was denied access to its migration lock and succeeded
with sandbox escalation. Git's filesystem monitor emitted IPC diagnostics;
explicit status/diff checks used `core.fsmonitor=false`. No tests failed.
An initial Rust formatting check failed on assertion wrapping and was fixed
before the successful final checks. No broad audit, push, merge, release,
cutover, or shared-ledger update was performed.
