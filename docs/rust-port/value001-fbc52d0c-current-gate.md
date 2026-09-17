# VALUE-001 — current candidate gate (`fbc52d0c`)

> **Decision (2026-09-16):** `RUST-006` is **blocked**. Its dependent items
> `RUST-007` and `RUST-016` are not ready. See
> [#936](https://github.com/danieljustus/symaira-desktop/issues/936).

This is a current-candidate decision, not a repin or a revision of historical
VALUE-001 evidence. Go remains the production implementation.

## Fresh measurement

The clean candidate `fbc52d0ca07a9bbca324a342263ad86dd05adf52` was measured
three times on the current macOS arm64 host against immutable Go oracle
`745c08e8144971c61133c5d0e5d61c7ce405aad2`. Each run used 100 post-warmup
samples and 20 warmups, the same Rust release binary, a separate clean
worktree, a synthetic document vault, loopback-only dynamic ports, and isolated
HOME/XDG/TMP roots.

| Run | Formal VALUE-001 result | `http.healthz` paired-median regression | Artifact SHA-256 |
| --- | --- | ---: | --- |
| 1 | FAIL | +43.99% | `4fff7f0add6c6a60f7ab4ba4b6435006cc9e4be313df22b1ed040cb4ded57022` |
| 2 | PASS | -3.66% | `4c90cd0602d3d61312a36a731c619ad2bd62d7dbd8c32df99a8239746f5808f0` |
| 3 | FAIL | +42.19% | `17e80cd5e6c06c4621a3309684c252f55acf7fd19a41e6688302796fee077f93` |

All three runs passed the four representative semantic differential contracts.
No raw timing was zero, non-finite, or timeout-like. The Rust binary SHA-256
was `2ed856124028ad1b0874b78582bbe1fde0820f1252dfcfad89b05b75a4020d98`.

## Why this is blocked, not a cherry-picked pass

The alternating-order cohorts are consistently different:

| Request order | Median Rust/Go ratio across runs |
| --- | ---: |
| `go-rust` | 0.400 / 0.392 / 0.443 |
| `rust-go` | 2.152 / 2.204 / 2.132 |

The current `paired_median_ratio` pools those two heterogeneous cohorts. With
an even sample count, the boundary between the two clusters makes the formal
result flip between PASS and FAIL. This is a systematic order bias, not an
ignorable outlier.

The #936 repair changes **new candidate approval** to
`order_stratified_paired_median_ratio`: it computes the paired median for
`go-rust` and `rust-go` separately, records both values, and gates on the
worse cohort. Missing, mismatched, or materially unbalanced order labels fail
closed. Historic reports retain their original declared estimator for audit,
but cannot approve a fresh candidate. A new full measurement still has to pass
under that rule before `RUST-006` can change state.

## Repair verification (not a formal approval)

The initial schema-4 repair was checked in the assigned worktree. The focused
`value-001-evidence-tests` target then passed 139 tests, including the
order-cohort, strict failure-boundary, schema-compatibility, validator, and
report cases. The JSON Schema 2020-12 document also validated a schema-4
fixture.

## Controlled HTTP scheduling correction (2026-09-17)

The initial pairing still scheduled all seven HTTP routes against one server
before the other, so a route's first-request position was fixed. Schema 5
rotates the routes every round and alternates Go/Rust per route. The validator
reconstructs that exact schedule; schema-4 evidence remains readable but cannot
approve a current candidate.

`PYTHONDONTWRITEBYTECODE=1 make value-001-evidence-tests` passed 153 tests.
No fresh benchmark or native build was run, so there is no new performance
result or approval evidence; `RUST-006`, `RUST-007`, and `RUST-016` remain
blocked. The source-bound validator now requires exact schema-5 evidence, the
controlled order-stratified schedule, unchanged thresholds, candidate-root
provenance, and a clean candidate checkout. Historical schema-2/3/4 evidence
retains its original semantics and cannot be promoted.

The raw host-specific captures are deliberately retained outside the repository.
This document contains only the reproducible candidate/oracle identities and
artifact digests.

## Native-record check

Three fresh isolated `symdesk version --json` invocations from the measured
Rust binary produced identical valid JSON (`symdesk` version `0.12.2`) with
empty stderr. The source tree contains no `SYMDESK_NATIVE_RECORD` consumer,
so this confirms fresh JSON version output only; it is **not** an integrated
native-record contract. Adding or claiming such a contract requires a separate
scoped change.

## Scope boundary

PRs [#927](https://github.com/danieljustus/symaira-desktop/pull/927) and
[#930](https://github.com/danieljustus/symaira-desktop/pull/930) integrated
frontmatter/history and pinned-Go-oracle foundations. They are not RUST-007
acceptance evidence. No production cutover, Go removal, release, or downstream
Rust slice is authorized by this checkpoint.
