# VALUE-001 per-operation repair — issue #897

This bounded repair starts from `2176334fa6b0b4667b48460728eeb4d7d684beb6`.
That snapshot already applies the unchanged `0.10` p95 regression ceiling to
startup, search, aggregate MCP/HTTP, and all five MCP and seven HTTP operations
in the producer and candidate, retained, and resumption validators. This change
adds the missing producer summary check and executable regression coverage.

The producer now rejects `min`, `mean`, `p50`, `p95`, or `p99` values that differ
from the raw samples. Its existing maximum checks remain in place. It raises an
error without replacing the inconsistent values. All acceptance validators
already call this producer validation, alongside their provenance checks.

The evidence-test Make target now discovers all five VALUE-001 test modules,
including the previously omitted retained-validator and report tests. Controls
cover each of the 16 latency gates, operation regressions hidden by passing
aggregate summaries, altered summaries, missing required operations, and the
historical resumption failure. Candidate and retained tests supply coherent
raw samples, summaries, and ratios and assert the latency rejection reason.
Resumption mutation tests isolate only the digest anchor on temporary test
copies so an integrity failure cannot mask an absent operation check; the real
immutable capture is also tested without mocking its anchor.

Producer tests use retained raw arrays, deliberate in-memory mutations, inert
size-only files, and mocked Git/toolchain metadata. They do not build or execute
Go/Rust binaries, time a workload, or save benchmark evidence. A large mock build
duration is excluded from the asserted latency inventory. Ratios stay
dimensionless; only report text multiplies them by 100 for display.

## Checkpoint verification

- Baseline focused discovery: 35 tests passed.
- Before the producer repair, its four new test methods ran and the altered
  summary control failed in all 160 subcases, demonstrating the missing check.
- `PYTHONDONTWRITEBYTECODE=1 make value-001-evidence-tests`: 47 tests passed,
  followed by the historical retained validator and both retained report CLIs.
- `ruff check --no-cache scripts/rust-port/test_*value001*.py`: passed.
- `ruff format --no-cache --check scripts/rust-port/test_value001_producer.py`
  and range checks of the added code: passed.
- All ten VALUE-001 Python modules parsed successfully; `git diff --check`
  passed. Retained artifacts and metadata matched the starting commit byte
  for byte (all seven files).
- Full VALUE-001 Ruff lint still reports the existing `F841` unused
  `contract_root` in `value001.py`. The starting commit produces the identical
  diagnostic. This repair adds no lint suppression or unrelated cleanup.
- Go tests executed: zero. The slice changes Python validation/tests, its Make
  test command, and this note; no Go code or Go harness contract changes.
  `dev-external --status` confirmed the external volume mounted with 68 verified
  links and reported the Go build cache as internal. No builds were run.

## Acceptance remains stopped

The retained resumption capture at
`956bd3e008b3fc08e9e3c01f46a577d2f42b4235` still fails per-operation acceptance:
`http.file-read` is **17.9074%** slower and `http.file-missing` is **12.6140%**
slower, although aggregate HTTP is **2.8530%** faster. Its historical recorded
pass flag, raw samples, hashes, metadata, and source identity remain unchanged.

Passing these code tests does not approve the current candidate or complete
issue #897's independent current-source evidence requirement. No fresh benchmark
was run. Go remains production, VALUE-001 remains stopped, and later migration
stages remain blocked. The workload, threshold, shared operation ledgers,
dependencies, release/cutover configuration, and external systems are unchanged.
