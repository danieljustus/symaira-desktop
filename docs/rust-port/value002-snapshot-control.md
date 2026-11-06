# VALUE-002 `http.snapshot` control experiment (owner-requested, preregistered)

Diagnostic only. It does not decide VALUE-002, change a threshold, or replace a
gate run. Owner decision (2026-11-06): investigate the run-4 finding with a
fixed-order control experiment; fix a real Rust cost if one exists, then make a
new candidate and exactly one gate run.

## Finding under investigation

Runs 1, 3 and 4 on candidate `8ba6eb5b`, `http.snapshot` order cohorts:

| Run | `go-rust` (Rust second) | `rust-go` (Rust first) |
| --- | ---: | ---: |
| 1 | -31.68 % | +10.49 % |
| 3 | -42.05 % | +7.55 % |
| 4 | -38.26 % | +39.51 % (lower bound +22.98 %, FAIL) |

The side requested second is faster in every run. The snapshot body carries all
10,001 documents with content, so the timed interval includes a multi-megabyte
body read and decode by the Python client.

## Method

`scripts/rust-port/value002_snapshot_control.py` with the measured binaries of
the VALUE-002 build record (`measured/value002-8ba6eb5b/{go,rust}/symdesk`), the
unchanged value001 vault generator, index preparation and server wrapper, 100
timed rounds after 10 warmups per block, 5 ms pacing, healthz priming per round.
Every request records time-to-headers and total time separately. Blocks:
`fixed-go-rust`, `fixed-rust-go`, `isolated-go-first`, `gate-fixed-go-rust`,
`gate-fixed-rust-go` (the gate's seven-operation rotation, order fixed), then
`fixed-rust-go#2`, `fixed-go-rust#2`, `isolated-rust-first` (reversed block
order, to separate drift from position).

## Preregistered reading (written before the first run)

* **Positional artefact:** each side's median total time drops when it is in
  second position in both fixed blocks, and the isolated blocks put Rust at or
  below Go. Then the cohort gap is a property of the paired schedule, not of the
  Rust server, and there is nothing to fix in the product.
* **Client-side origin:** if the position effect is in total time but not in
  time-to-headers, the cost sits in the client's body read, not in the servers.
* **Real Rust cost:** Rust is slower than Go in the isolated blocks, or in
  time-to-headers in both fixed blocks. Then the Rust snapshot path is profiled
  and fixed, a new candidate is built and exactly one gate run decides.

Results are appended below unchanged, including if they contradict the
hypothesis.

## Results
