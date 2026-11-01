# CLI-007 — signals, stdin/stdout closure and child processes

Contract row: `CLI-007` in `contract-matrix.md`. Oracle: the real Go `symdesk`
binary, recorded by `cmd/symdesk/signals_process_port_test.go`
(`make cli-signals-fixtures-generate`) into
`testdata/port/cli/signals-process.json`; replayed against the Rust binary by
`crates/symdesk-cli/tests/signals.rs` (`make cli-signals-differential`). POSIX
only; Windows has no signal semantics here.

Each case starts the binary in its own process group, waits for a readiness
signal (a stderr line, an accepting port, a fake server request or a file the
fake child writes), then sends a signal or closes stdin/stdout and compares the
eventual state: exit status or terminating signal, stdout, stderr (Go slog lines
removed), whether the process was still running afterwards, the requests a fake
server saw and whether a spawned child survived. Durations are never compared;
every case only has a generous ceiling.

## Recorded behaviour (Go = Rust unless listed under "Declared differences")

| Command | SIGINT / SIGTERM | stdin EOF | external SIGPIPE | write to a closed stdout |
| --- | --- | --- | --- | --- |
| `mcp` | no handler: ends **by the signal** | finishes in-flight calls, exit 0 | ignored | ends **by SIGPIPE**, no stderr text |
| `serve` | graceful shutdown (listener closed, connections drained), exit 0 | ignored | ignored | n/a |
| `worker` | cancels the in-flight lease/download/OCR request and stops a running OCR child, exit 0 | ignored | ignored | n/a |
| `events` | exit 0 after closing watcher and sidecar | exit 0 | ignored | not replayed (Go dies on the first event; Rust ends by SIGPIPE like `mcp`) |
| `sources watch` | no handler: ends **by the signal** | ignored | ignored | n/a |

* Flushing: an MCP answer written before stdin EOF is on stdout before the
  process exits 0.
* `worker` keeps polling after a failed poll or job until a signal arrives;
  `--once` returns the error (exit 1) or 0.
* Children: a completed `hermes` child is reaped. When `ask`/`mcp desk_ask` is
  ended by a signal, the `hermes` child is **not** killed in Go (no handler, no
  process-group kill) and stays alive; Rust does the same. This is recorded as
  `child_alive_after_parent_exit = true` and is the one place where Go leaves an
  orphan. Changing it would be a deliberate divergence and needs a decision.
* The OCR child of `worker` is stopped on SIGINT/SIGTERM (Go
  `exec.CommandContext`).

## Declared differences and non-comparable items

* `events`: Go unlocks an unheld mutex on the first filesystem event and dies
  with `fatal error: sync: unlock of unlocked mutex` (exit 2). The fixture keeps
  that recording (`go_defect`); the Rust replay uses the declared `rust`
  override: it prints the NDJSON line and keeps running until SIGTERM (exit 0).
  The pure event processor (`processEvent`) is replayed against 11 Go-recorded
  operation/output/sidecar cases in `events_cli.rs`.
* `events` does not start the Go inbox and mail watchers (ingest); the inbox
  directory is still created. A removed document is deleted from the sidecar
  only (Go also asks the optional retrieval index).
* `worker` implements the Tesseract engine and the lease/download/complete/fail
  protocol. `--engine ollama` jobs fail with an explicit error and the OCR
  plausibility retry is not ported (HTTP worker routes belong to RUST-015).
  Transport error text is `ureq`'s, not `net/http`'s.
* `serve`: Go drains idle and never-used connections for about five seconds
  before exiting 0; Rust drains them at once (still exit 0, port closed). Rust
  prints a `LISTENING http://ADDR` readiness line that Go does not; it is
  dropped before comparison.
* Closing stderr (SIGPIPE on fd 2) is not replayed.
* Timing: no duration, debounce interval or poll cadence is compared.
