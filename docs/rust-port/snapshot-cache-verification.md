# Snapshot-cache verification

## Scope

PR #885 merged as `d66c4557f3fcf04c6c3128050ecea487b2ae892a`.
The tested PR head was `655d24869bc0e8974087f0b7fb9e4fefd8f9cc18`.
A post-merge comparison found no changes to Cargo manifests/lockfile, Rust
crates or the VALUE-001 runner between those revisions.

## Native verification

Full explicitly dispatched CI on the tested head passed:
https://github.com/danieljustus/symaira-desktop/actions/runs/34238241000

This includes native Rust and port-contract jobs on Linux, macOS and Windows,
Go test jobs, macOS and iOS application tests, Rust hardening and fuzz smoke.
The separate PR checks, including CodeQL, also passed before merge.

## Clean-source VALUE-001

The runner recorded an empty source status at the tested head, 100 samples
and 20 warmups per defined workload. HTTP has 700 raw observations per
implementation. The parent independently recomputed nearest-rank p95 and
all latency ratios from the raw arrays.

| Metric | Go p95 (ms) | Rust p95 (ms) | Regression ratio |
|---|---:|---:|---:|
| Startup | 19.221208 | 10.428667 | -0.4574395636320048 |
| Search | 66.763625 | 13.388667 | -0.7994616529584786 |
| MCP | 64.285167 | 23.369875 | -0.6364655162208726 |
| HTTP | 1.03 | 0.911083 | -0.1154533980582525 |

The runner reports `passed: true`; the minimum improvement remains 0.20 and
maximum p95 regression remains 0.10. Binary-size reduction is
0.8494741315283268. These are dimensionless ratios, not display percentages.

The preserved raw capture remains outside the repository at
`/tmp/value001-snapshot-655d248-verified.json`.
Original SHA-256: `d35eac7b86d0c00eef68c94388266059cc327e2c9356ea69ac6598e223bc43f5`.
The privacy-reviewed durable derivative is
`docs/rust-port/results/value001-retained.json`; its SHA-256 is
`bfc4f2274b3d2a2ea8881e014cbb350c243420322c4bd43c5fbf58c260d9988c`.
The sidecar records the transformation and trusted CI run. Validate it with
`make value-001-validate`; this does not advance the work-item gate.
The historical failed `results/value001-latest.json` remains byte-exact and
is still exercised by the report helper.

## Remaining boundaries

### Read-error and race regression checkpoint

Issue #889 tracks the Linux CI race-test failure at `3c81416e`:
https://github.com/danieljustus/symaira-desktop/actions/runs/34251843055/job/102147904577

The corrected patch is based on `fd43a3be`. Production snapshot reads still
propagate errors, matching Go `internal/selfhost/snapshot.go`; a transient
read error must never publish a successful partial snapshot. Deterministic,
per-cache test injection exercises partial reads, handler HTTP 500, retained
old payload, dirty state, changed-content retry and subsequent hot reuse.
The Unix race test checks successful and partial bytes and uses RAII to stop
and join its mutator. This stress test is probabilistic, not exhaustive proof.

Local macOS verification passed: `cargo fmt --all --check`,
`cargo test -p symdesk-protocol --all-features --locked` (24 tests), and
`cargo clippy -p symdesk-protocol --all-targets --all-features --locked -- -D warnings`.
Independent review approved these exact source hashes:

- `lib.rs`: `75ec955b96f5da6cc6e99e4ead739731b3c5581d59117d25193faa788381210f`
- `snapshot_cache.rs`: `0ef5cee7b54ecd97b67be454c32b5f1fd370afd9f7d7aad64ef217e4d233b606`
- `snapshot_cache_contracts.rs`: `705a7be071281eb7da2bacb6fa03d2324c8f93a18256f664a9caa6b58a12bccd`

Native Linux/Windows CI, full Rust gates, representative differential tests
and current-revision VALUE-001 reconciliation remain pending. RUST-006 is
not advanced by this local checkpoint; RUST-007 and its existing unintegrated
atomic-write/history-generator work remain blocked. Next: pass #889 CI and
the RUST-006 acceptance commands before recovering RUST-007. Go remains production.

### Scope limits

- Early representative parity is not full CLI/MCP/HTTP or product parity.
- Record an actual Go-oracle invalid-UTF8 filename fixture before changing
  the Rust adapter's pre-existing explicit rejection behavior.
- The retained payload cap does not bound cold-build allocations; changing
  the response size contract requires coordinated Go/Rust hardening.
- A replaced root permanently disables hot hits until server restart.
- No production cutover, registry publication, stable release or Go removal
  is authorized by this report alone.
