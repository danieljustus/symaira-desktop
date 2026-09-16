# VALUE-001 Representative Acceptance — Commit `aeab7664`

## Scope and Decision

This document records the fail-closed VALUE-001 representative acceptance for the clean measured Rust candidate commit `aeab76640b5e2f1eb935bd3ec2173ed1c608bf06` compared against the frozen Go current-behaviour oracle commit `745c08e8144971c61133c5d0e5d61c7ce405aad2` (version `0.12.2`).

Acceptance applies strictly to work item **RUST-006** (representative CLI, MCP, and HTTP slice). It does not approve full cutover, mark downstream work items complete, or authorize removal of the Go backend.

> **Historical-status note:** This is immutable acceptance evidence for
> `aeab7664`, not approval of current HEAD. The fresh `fbc52d0c` gate is
> blocked by #936, so RUST-007 and RUST-016 are not currently ready. The DAG
> wording near the end records the status at this historical acceptance point.

## Measured Commit vs. Documentation Commits

Approval is bound to the exact immutable Git commit `aeab76640b5e2f1eb935bd3ec2173ed1c608bf06`. This retention change alters no production Rust or Go code. Its evidence remains bound to that measured candidate; later production candidates require their own measurements.

The fixed validator (`scripts/rust-port/validate_value001_aeab7664.py`) requires an explicit `--root` parameter pointing to a clean detached worktree at `aeab76640b5e2f1eb935bd3ec2173ed1c608bf06` to prevent approving arbitrary current HEAD revisions.

## Provenance and Integrity

- **Clean Measured Candidate**: `aeab76640b5e2f1eb935bd3ec2173ed1c608bf06`
- **Current Behaviour Oracle**: `745c08e8144971c61133c5d0e5d61c7ce405aad2` (Go 1.26.6, version `0.12.2`)
- **Original Raw Capture SHA256**: `8acdbc8415f4f5ee89d26161913ab31ccdcda87517c9b1cbb10648c7733a14d5` (`consumers-night-desktop-value-aeab7664-001.json`)
- **Derived Trusted SHA256**: `b34ec7f8fddbadd3d63ccec3cc9416cb9335e40aa9b456f6d81cd1c5be067f31` (`docs/rust-port/results/value001-aeab7664.json`)
- **Provenance Sidecar**: `docs/rust-port/results/value001-aeab7664.metadata.json`
- **Integrated CI Run**: `34904301453` (SUCCESS)
- **Native Source CI Run**: `34902569823` (SUCCESS)
- **Previous evidence controls**: 71 tests passed; this is not the native/full-product test count.
- **Retained-evidence controls**: independently executed `make value-001-evidence-tests`, 103 tests passed. The fixed validator also passed against the original private raw capture and clean measured checkout.
- **Supplementary p95 negative control**: removing the real candidate validator's p95 guard caused all 16 tail-regression subcases to fail with `ValidationError not raised`; restoring the guard restored the complete passing evidence suite. No retained artifact bytes changed.

### Path-Only Privacy Derivation

Recursive comparison (`compare_derivation`) confirmed that only the 9 approved path-prefix fields were transformed from the raw capture:
- `$.binaries.go.build_command`
- `$.binaries.go.path`
- `$.binaries.rust.path`
- `$.contracts[1].command`
- `$.contracts[2].command`
- `$.contracts[3].command`
- `$.index_preparation.go.command`
- `$.index_preparation.rust.command`
- `$.repository.root`

All paths are sanitized using standard `<redacted-repository>` and `<redacted-temporary-directory>` prefix substitutions. No raw sample values, pair orders, summary statistics, binary source commitments, binary hashes, or pass/fail thresholds were altered.

## Measurement Workload & Methodology

- **Run Configuration**: ONE full, non-cherry-picked run consisting of 100 sample pairs and 20 warmup iterations per operation.
- **Pairing Scheme**: Alternating `go-rust` and `rust-go` order per post-warmup round to prevent systematic warm/cold drift bias.
- **Workload**: 10,000-document deterministic synthetic Markdown vault (~1.46 MB generated corpus), with cohort 42 search anchor token `value001cohort042` (100 matches).
- **Semantics**: Full semantic payload validation on every timed invocation across CLI, MCP, and HTTP protocols.

## Gate Criteria & Measured Reductions

All thresholds require the unchanged latency ceiling of **10.0%**. At least one of binary size or representative peak RSS must improve by **20.0%**; both are reported but both are not mandatory. The declared estimator is `paired_median_ratio`; the validator also applies a mandatory supplementary `unpaired_p95` check to the same raw samples.

| Metric Category | Gate / Requirement | Measured Result | Verdict |
| :--- | :--- | :--- | :--- |
| **Binary Size Reduction** | $\ge 20.0\%$ | **84.8043%** (Go 23,640,322 B $\to$ Rust 3,592,304 B) | **PASS** |
| **Peak RSS Reduction** | $\ge 20.0\%$ | **71.0964%** (Go 66,945,024 B $\to$ Rust 19,349,504 B) | **PASS** |
| **Contract Checks** | Exit code 0, empty stderr | 4/4 contracts passing (`representativegen`, `diffharness`, `mcpdiff`, `httpdiff`) | **PASS** |
| **Startup Latency** | $\le +10.0\%$ regression | **-47.0008%** (Rust is 47.0% faster) | **PASS** |
| **Search Latency** | $\le +10.0\%$ regression | **-79.2786%** (Rust is 79.3% faster) | **PASS** |
| **MCP Aggregate** | $\le +10.0\%$ regression | **-49.5825%** (Rust is 49.6% faster) | **PASS** |
| • `mcp.initialize` | $\le +10.0\%$ regression | **-46.7784%** | **PASS** |
| • `mcp.tools-list` | $\le +10.0\%$ regression | **-48.6567%** | **PASS** |
| • `mcp.desk_status` | $\le +10.0\%$ regression | **-47.5424%** | **PASS** |
| • `mcp.desk_ls` | $\le +10.0\%$ regression | **-49.9412%** | **PASS** |
| • `mcp.desk_search` | $\le +10.0\%$ regression | **-80.0807%** | **PASS** |
| **HTTP Aggregate** | $\le +10.0\%$ regression | **-13.2949%** (Rust is 13.3% faster) | **PASS** |
| • `http.healthz` | $\le +10.0\%$ regression | **-25.6638%** | **PASS** |
| • `http.status` | $\le +10.0\%$ regression | **-21.8266%** | **PASS** |
| • `http.snapshot` | $\le +10.0\%$ regression | **-24.5499%** | **PASS** |
| • `http.file-read` | $\le +10.0\%$ regression | **-9.8908%** | **PASS** |
| • `http.file-range` | $\le +10.0\%$ regression | **-11.0974%** | **PASS** |
| • `http.file-missing` | $\le +10.0\%$ regression | **-5.8669%** | **PASS** |
| • `http.file-traversal` | $\le +10.0\%$ regression | **-13.9456%** | **PASS** |

### Latency estimator audit from the retained report

These are two separate recomputations over the real retained report. The actual marginal P95 check and the declared paired-median estimator are not the same statistic.

#### Actual marginal `unpaired_p95` check

| Gate | Regression |
| :--- | ---: |
| `startup` | -59.44% |
| `search` | -82.10% |
| `mcp` | -67.71% |
| `mcp.initialize` | -46.01% |
| `mcp.tools-list` | -44.61% |
| `mcp.desk_status` | -43.07% |
| `mcp.desk_ls` | -47.45% |
| `mcp.desk_search` | -82.74% |
| `http` | -25.64% |
| `http.healthz` | -34.88% |
| `http.status` | -26.12% |
| `http.snapshot` | -21.59% |
| `http.file-read` | -27.67% |
| `http.file-range` | -8.55% |
| `http.file-missing` | -18.85% |
| `http.file-traversal` | -19.48% |

#### Declared `paired_median_ratio` check

| Gate | Regression |
| :--- | ---: |
| `startup` | -47.00% |
| `search` | -79.28% |
| `mcp` | -49.58% |
| `mcp.initialize` | -46.78% |
| `mcp.tools-list` | -48.66% |
| `mcp.desk_status` | -47.54% |
| `mcp.desk_ls` | -49.94% |
| `mcp.desk_search` | -80.08% |
| `http` | -13.29% |
| `http.healthz` | -25.66% |
| `http.status` | -21.83% |
| `http.snapshot` | -24.55% |
| `http.file-read` | -9.89% |
| `http.file-range` | -11.10% |
| `http.file-missing` | -5.87% |
| `http.file-traversal` | -13.95% |

## Executable Verification Commands

### 1. Direct Artifact Validation against Clean Candidate Worktree

```sh
# Create one uniquely owned private detached worktree and clean only it.
candidate_worktree=$(mktemp -d "${TMPDIR:-/tmp}/symdesk-value001-aeab-XXXXXX")
cleanup() {
  git worktree remove --force "$candidate_worktree" >/dev/null 2>&1 || true
}
trap cleanup EXIT
git worktree add --detach "$candidate_worktree" aeab76640b5e2f1eb935bd3ec2173ed1c608bf06

# Run the fixed aeab7664 acceptance validator without bytecode artefacts.
PYTHONDONTWRITEBYTECODE=1 python3 scripts/rust-port/validate_value001_aeab7664.py --root "$candidate_worktree"
```

### 2. Full Evidence Test Suite Discovery

```sh
# Discovers test_validate_value001_aeab7664.py along with all existing VALUE-001 tests
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts/rust-port -p 'test_*value001*.py' -v
```

## Historic DAG progression at the measured commit

Following the passing acceptance of RUST-006:
- **RUST-006**: Status updated to `passed`.
- **RUST-007** (*Port vault writes history trash conflicts and retention*): Dependencies satisfied (`RUST-006`), status updated to `ready`.
- **RUST-016** (*Port SymRoom core CLI MCP and signed journal*): Dependencies satisfied (`RUST-006`), status updated to `ready`.
- **RUST-008** (*Port full sidecar and retrieval stack*): Depends on both `RUST-006` and `RUST-007`; remains `blocked` until `RUST-007` passes.
- **RUST-009 through RUST-015 and RUST-017 through RUST-019**: Remain `blocked`.
