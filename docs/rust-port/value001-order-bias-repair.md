# VALUE-001 order-bias repair

**Status:** local harness repair verified; this document does not approve RUST-006. Go remains production and the current candidate is blocked.

## Problem

VALUE-001 measures Go and Rust as adjacent pairs and alternates their execution
order. A pooled median of all pair ratios can conceal a stable second-invocation
slowdown when one order is fast and the other is slow. It is not a valid approval
statistic for a new candidate.

## Schema contract

- **Schema 2** is pre-declaration historical evidence. It retains its original
  unpaired-p95 interpretation and provenance checks.
- **Schema 3** is immutable historical declared-estimator evidence. It retains
  only `unpaired_p95` or `paired_median_ratio` semantics.
- **Schema 4** is required for a current candidate. It requires
  `order_stratified_paired_median_ratio`; schema 2 or 3 cannot be promoted by
  passing a current-validator flag.

Historical artifacts are not rewritten or reinterpreted under schema 4.

## Schema-4 latency decision

For every aggregate and operation gate:

1. Rust/Go raw samples must be index-aligned and strictly positive finite
   numbers.
2. Both sides must carry identical `pair_order` arrays.
3. Samples are partitioned into `go-rust` and `rust-go` cohorts.
4. Each cohort needs at least 50 samples; cohort counts may differ by at most
   one.
5. The gate value is the worse cohort's median of `rust / go - 1`.
6. Each order-specific value must be `<= 0.10`; no epsilon is applied.

`thresholds.latency_regressions` records that scalar worst cohort.
`thresholds.latency_order_regressions` records both cohorts. Descriptive median
intervals live under `latency_order_regression_intervals` and remain separated
by order; there is no pooled schema-4 interval or supplementary pooled-p95
approval gate.

## Failure boundary

For **schema 4**, zero, negative, boolean, non-finite and sentinel timing
values are invalid. Schema 2 and 3 keep their historical non-negative sample
format and are never reinterpreted under the newer rule. Process and HTTP
timeouts become controlled `HarnessError` failures. A failed run leaves an
atomically exclusive `<output>.incomplete` marker; formal candidate validation
rejects an artifact while that marker exists. Completed artifacts are
self-validated and atomically replaced. Formal candidate validation also
requires an externally supplied digest, exact clean candidate checkout, schema
4 and the order-stratified contract.

## Verification

Run:

```sh
PYTHONDONTWRITEBYTECODE=1 make value-001-evidence-tests
VALUE_OUTPUT=/absolute/path/new-result.json make value-001
```

A non-zero exit from the second command can be a correct fail-closed benchmark
result. Inspect the emitted JSON, validate its schema and compare repeated full
runs before changing RUST-006 status.
