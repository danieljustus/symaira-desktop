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

Two full runs on 2026-11-06 (load1 3.8-5.0, alfaview open), retained as
`results/value002-snapshot-control-{1,2}.json`. Medians in ms, 100 timed
requests per side and block; body 2,489,121 (Go) / 2,489,132 (Rust) bytes.

| Run | Block | Go headers / total | Rust headers / total | Rust/Go headers | Rust/Go total | paired total ratio |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| 1 | `fixed-go-rust` | 1.716 / 3.947 | 1.616 / 3.323 | 0.942 | 0.842 | 0.984 |
| 1 | `fixed-rust-go` | 1.985 / 4.932 | 2.368 / 4.714 | 1.193 | 0.956 | 1.009 |
| 1 | `isolated-go-first` | 2.124 / 4.802 | 1.152 / 2.424 | 0.542 | 0.505 | 0.544 |
| 1 | `gate-fixed-go-rust` | 1.002 / 2.660 | 1.080 / 2.495 | 1.077 | 0.938 | 1.054 |
| 1 | `gate-fixed-rust-go` | 0.965 / 2.500 | 1.256 / 2.788 | 1.302 | 1.115 | 1.072 |
| 1 | `fixed-rust-go#2` | 1.555 / 3.266 | 1.547 / 3.273 | 0.995 | 1.002 | 1.009 |
| 1 | `fixed-go-rust#2` | 1.486 / 3.635 | 1.565 / 3.447 | 1.053 | 0.948 | 0.986 |
| 1 | `isolated-rust-first` | 1.171 / 2.786 | 1.655 / 3.712 | 1.413 | 1.332 | 1.178 |
| 2 | `fixed-go-rust` | 1.710 / 3.776 | 1.741 / 3.766 | 1.019 | 0.997 | 1.037 |
| 2 | `fixed-rust-go` | 1.911 / 4.137 | 2.019 / 4.305 | 1.057 | 1.041 | 0.977 |
| 2 | `isolated-go-first` | 1.607 / 3.631 | 0.660 / 1.714 | 0.411 | 0.472 | 0.484 |
| 2 | `gate-fixed-go-rust` | 1.076 / 3.089 | 1.245 / 2.836 | 1.157 | 0.918 | 1.032 |
| 2 | `gate-fixed-rust-go` | 1.492 / 3.851 | 1.813 / 3.642 | 1.215 | 0.946 | 0.961 |
| 2 | `fixed-rust-go#2` | 1.765 / 3.902 | 1.914 / 3.924 | 1.084 | 1.005 | 0.939 |
| 2 | `fixed-go-rust#2` | 1.887 / 4.411 | 2.169 / 4.421 | 1.149 | 1.002 | 1.063 |
| 2 | `isolated-rust-first` | 1.252 / 3.119 | 1.889 / 3.622 | 1.508 | 1.161 | 1.036 |

### Reading against the preregistration

* **The gate's cohort gap does not reproduce with a fixed order.** In the eight
  fixed snapshot-only blocks the Rust/Go total ratio is 0.84-1.04 and the paired
  median 0.98-1.04, against -38 % / +40 % in the gate's alternating cohorts. The
  gate regime with a fixed order gives 0.92-1.12 (paired 0.96-1.07). Same
  outcome as the VALUE-001 control in #936.
* **The positional hypothesis as preregistered is not confirmed either:** the
  side in second position is not consistently faster once the order is fixed.
  The effect needs the gate's per-round alternation.
* **Isolated blocks are dominated by block order, not implementation:** whichever
  side runs in the first half is slower (Go-first: Rust/Go 0.505 and 0.472;
  Rust-first: 1.332 and 1.161). Averaged over both block orders Rust is faster:
  total 3.07 vs 3.79 ms (run 1) and 2.67 vs 3.38 ms (run 2); time-to-headers
  1.40 vs 1.65 ms and 1.27 vs 1.43 ms.
* **Time-to-headers, the preregistered "real cost" signal:** in run 2 Rust is
  2-15 % slower to headers in all four fixed snapshot-only blocks while total time
  is equal; in run 1 the sign is mixed (0.94-1.19). The criterion is met in one
  run of two, so it is reported as weak and not reproduced.
* **Code check (`crates/symdesk-protocol`):** the handler serves the cached
  `Bytes` (reference-counted clone, no copy, no re-serialisation); per request it
  takes the cache mutex and opens the vault root once for a `dev:ino` identity
  check (one `open` + `fstat`, microseconds against a 1-4 ms request). No
  per-request cost of the size of the gate finding is present.

**Conclusion:** no Rust snapshot cost was identified that a code change could
remove. The run-4 failure is a property of the gate's alternating schedule on
this route, as in #936. Nothing was changed in the product, the thresholds or the
gate; VALUE-002 stays FAIL for `8ba6eb5b` and the next step is an owner decision.
