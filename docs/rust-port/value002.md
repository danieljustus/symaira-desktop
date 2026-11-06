# VALUE-002: full value gate for complete release candidates

VALUE-002 re-applies the **fixed** [VALUE-001](README.md#why-rust--the-stop-rule)
thresholds to the dual-binary release candidate (`symdesk` + `symroom`) under
fuller workloads. No threshold, estimator or metric is redefined; a FAIL is
recorded as FAIL and the port stops instead of moving the goalposts.

## Decision rule (unchanged from VALUE-001)

| Criterion | Rule |
|---|---|
| Improvement | combined `symdesk`+`symroom` release binaries **>= 20 % smaller**, OR **>= 20 % lower** maximum RSS under the representative workload (VALUE-001's long-running HTTP server RSS) |
| Latency | startup, indexed search, HTTP and MCP p95 may not regress by more than **10 %**, decided on the schema-6 order-stratified 95 % median interval (`value001.order_stratified_latency_failure`: a cohort fails when its interval lower bound exceeds `+0.10`, or when an interval is wider than `0.40`) |
| Contracts | the four representative differentials (representative CLI, MCP, HTTP, fixture check) pass for the release pair |

The ratios are stored dimensionless (`rust/go - 1`); the verdict printer
multiplies by 100 for display only.

## What VALUE-002 adds (declared before any result existed)

* **Release candidates, not dev builds.** The Go pair is built with the
  GoReleaser flags (`CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w -X ..."`,
  `GOTOOLCHAIN=go1.26.10`); the Rust pair comes out of
  `scripts/release/build.sh darwin arm64 0.12.2 <dir>` (the real release path,
  target-triple build) and is measured **from its archive**. A Go archive is
  produced with the same `scripts/release/package.py archive` tool so archive
  sizes are comparable; the published Go v0.12.2 `darwin_arm64` archive size
  (12,629,014 bytes) is reported next to it. Archive sizes are reported; the
  size criterion is on the binaries.
* **VALUE-001 slice re-measured** with `value001.py`'s own functions
  (`measure_process_pair`, `measure_mcp`, `measure_http`, `prepare_index`) and
  validated by `value001.validate_result`, embedded as `representative`
  (schema-6 shape; its Go reference is the Go release built from the same
  candidate commit instead of the frozen oracle).
* **New workloads**, each a paired Go/Rust latency pair (100 samples, 20
  warmups, alternating `go-rust`/`rust-go`, untimed setup) decided by the
  **same** interval rule. All run on a deterministic 10,000-document *linked*
  vault (VALUE-001 documents plus a frontmatter `related` link and two body
  wikilinks per note, ground truth derived from the generator, never from a
  binary):

  | Workload | Command | Cold/warm | Validated against |
  |---|---|---|---|
  | `symroom-startup` | `symroom version` | - | exact `symroom 0.12.2` |
  | `index-build` | `ls --json` | **cold**: sidecar and `~/.local/share/symdesk` removed before every run (asserted) | exact path/title set |
  | `graph` | `graph --json` | warm | exact node set + exact edge set (20,000 body links) |
  | `backlinks` | `backlinks <target> --json` | warm | exact source list |
  | `relations-inverse` | `relations inverse <target> --json` | warm | exact `(source, property)` set (`_link`, `related`) |
  | `duplicates` | `duplicates --json` | warm | strict group shape; offline SimHash retrieval |

  For every workload the canonical output digest of Go and Rust must be
  identical and stable across all runs (determinism and parity proof).
  Peak RSS per workload is probed with `/usr/bin/time -l` (5 runs per side)
  and reported, not gated.
* **Sustained mixed load.** Per side, a `serve` HTTP server and a persistent
  `mcp` stdio session run concurrently for 3,000 HTTP requests (including 120
  full 10,000-note snapshots) and 1,500 MCP tool calls (`desk_status`,
  `desk_search`, `desk_ls`), all responses validated; RSS of both processes is
  sampled every 100 ms. Two rounds per side in alternating order. The maximum
  combined RSS is **reported**; it does not replace the representative-RSS
  criterion.
* **Host quietness is part of the evidence.** A final capture starts only if
  `uptime` 1-minute load < 6 and no `cargo`/`rustc`/`xcodebuild`/
  `swift-frontend`/`go-build` process runs; the validator re-checks the
  recorded pre-run snapshot. Load before/after and a 30 s load timeline are
  recorded.

## Provenance (fail closed)

`value002.py record-build` appends each build or packaging step (exact command,
env, exit code, toolchain, artifact path/size/SHA-256) to a build record bound
to one clean candidate commit/tree. `run` refuses to start unless the record
covers all required steps, the archive members are byte-identical to the
recorded artifacts, the contract tools match their recorded builds, and the
checkout differs from the candidate only in `scripts/rust-port/`,
`docs/rust-port/` and `Makefile` (so the measured binaries really correspond to
the candidate tree).

## Result schema (`schema_version` 1, `benchmark` `VALUE-002`)

Top level: `runner` (profile, samples, warmups, probe runs), `host` (load
before/after/timeline), `repository` (head, candidate, changed paths),
`build_record`, `binaries` (per side/member bytes + SHA-256), `archives`,
`vaults`, `representative` (a complete VALUE-001 schema-6 result),
`workloads` (name -> `pair`, `command`, `output_sha256`, `outputs_equal`,
`setup_runs`, `peak_rss_bytes`), `sustained` (runs with raw RSS series),
`thresholds` (all derived values, recomputed by the validator), `passed`.

`validate_value002.py RESULT --candidate SHA` recomputes every summary,
interval, threshold and verdict from the raw samples and the sustained RSS
series, re-validates the embedded VALUE-001 slice with VALUE-001's own
validator, and rejects smoke profiles, non-quiet starts, unequal workload
outputs, tampered builds and threshold edits. `value002-smoke-fixture.json`
is a 4-sample structural smoke capture used only as the unit-test positive
control; it is **not** evidence.

## Running

```sh
# builds are direct dev-external commands, recorded afterwards (see value002.py record-build)
make value-002-tests
make value-002 VALUE002_OUTPUT=docs/rust-port/results/value002-<sha8>.json
make value-002-validate VALUE002_OUTPUT=... VALUE002_CANDIDATE=<40-hex>
```

Smoke (any host, no evidence): `value002.py run ... --profile smoke --samples 4 --warmups 2`.

## Capture log (declared before each run)

**Run 1 (candidate `8ba6eb5b`, 2026-11-06 05:58-06:14 CET):** retained as
`results/value002-8ba6eb5b-run1.json`, `passed: false`. Improvement passed
(combined binaries 25.14 % smaller, representative RSS 54.09 % lower, sustained
mixed-load RSS 55.67 % lower), contracts and sustained checks passed, every
latency cohort's interval lower bound stayed below +10 %, but the run failed
closed on precision: `http.snapshot.go-rust` interval width 0.4769 > 0.40
(interval [-57.89 %, -10.20 %], i.e. Rust faster, but too wide to carry
acceptance information). The host was not quiet during the run: load1 4.94 at
start, timeline mean 12.07, max 22.76, 19.34 at the end (concurrent agent
sessions; `fseventsd` wedged at ~100 % CPU).

**Declared procedure for run 2 (written before it starts):** the precision rule
says such a run "carries no acceptance information"; it is not evidence of a
regression. Following the VALUE-001 practice of comparing repeated full runs
(`value001-order-bias-repair.md`), exactly **one** confirmation run is made on the
same candidate, same build record, same thresholds and same rule, started only
below load1 5.5. Run 2 decides VALUE-002: if it fails for any reason, VALUE-002
is FAIL and the port stops for a decision by the owner. Run 1 stays retained and
is reported next to run 2 either way; no further reruns.

**Run 2 (2026-11-06 06:24-06:39 CET): aborted, no result.** Started at load1
4.07. The measurement ran to the end, but the harness crashed in
`TemporaryDirectory.__exit__` (`OSError: [Errno 66] Directory not empty`: a
`.DS_Store` written by Finder into the scratch directory on the external volume),
so no result file and no verdict were produced and nothing was printed or seen.
This is a harness defect, not a measurement outcome. Fix: scratch cleanup
ignores such errors, and the runtime root moves to the internal disk
(`VALUE_RUNTIME_ROOT=/private/tmp/...`). Amendment, declared before the rerun:
because run 2 produced no data, it does not count as the confirmation run; the
single confirmation run is repeated under exactly the rules declared above (same
candidate, build record, thresholds and rule; start below load1 5.5). Its result
decides VALUE-002; if it fails or aborts again, VALUE-002 is FAIL.

**Run 3, the confirmation run (2026-11-06 07:01-07:16 CET): FAIL.** Retained as
`results/value002-8ba6eb5b-run3.json`, `passed: false`. Started at load1 4.47;
timeline mean 8.99, max 27.03. Improvement passed (binaries 25.14 % smaller,
representative RSS 36.54 % lower, sustained mixed-load RSS 50.48 % lower);
contracts and sustained passed; no cohort's interval lower bound exceeded +10 %.
Failed closed on precision again: `http.snapshot.rust-go` interval width 0.5016
> 0.40 (point +7.55 %, interval [-5.62 %, +44.54 %]); the other order was
-42.05 % [-60.29 %, -24.09 %].

**Verdict under the declared procedure: VALUE-002 FAIL for candidate
`8ba6eb5b`.** The port stops here for an owner decision. Observation, not a
verdict change: in both runs the only failing check is the precision rule on
`http.snapshot`, in alternating order cohorts, with Rust faster in the other
cohort, on a host that was busy throughout (mean load 9-12, peaks 23-27).
No run showed a demonstrated latency regression; none could exclude one either.

**Run 4, declared by owner decision (2026-11-06, before it starts).** The owner
chose a new run on a repaired host with the rule unchanged: same candidate
`8ba6eb5b`, same build record, thresholds, schema-6 rule and 0.40 width limit;
start only below load1 5.5 and load5 6.5. Host change against runs 1-3:
`fseventsd` (wedged at ~100 % CPU for 6 days) was restarted and now idles; the
owner's video-conference client (alfaview, ~0.6-0.8 core, steady) stays open by
choice and is recorded as known background load. Run 4 decides VALUE-002 for
this candidate; runs 1-3 stay retained and are reported next to it. A failure
or abort returns to the owner; no automatic rerun.
