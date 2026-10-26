# Persistent ingest queue (INGEST-001)

`Store` ports `internal/ingest/internal/store/store.go`'s SQLite document
identity and job state machine. Go production files are unchanged. All fourteen
migration files are copied byte-for-byte, including tables outside this slice;
Rust records the same migration versions and reopens existing databases.

## Pinned behavior

- Documents deduplicate by `(sha256, vault_root)`, with the legacy note-path
  prefix fallback. Ordinary job enqueue deliberately **does not deduplicate**;
  only active `reocr` jobs deduplicate by document ID.
- Claims are atomic SQLite `UPDATE ... RETURNING` statements. A pending job is
  immediately eligible. A failed job is eligible at **ten seconds inclusive**,
  provided attempts are below three. Every successful claim increments attempts.
- There is **no expiring lease** in this SQLite queue. Advancing the clock does
  not reclaim a running job. `reset_running_jobs` explicitly resets running
  statuses without clearing attempts, errors or timestamps. Opening/reopening a
  store alone does not reset anything.
- Complete/fail/retry do not validate the previous status. Missing IDs return
  Go's exact no-rows diagnostics (claim instead returns no job). Completion and
  terminal failure update the document transactionally. Manual retry resets
  attempts, error and document status, even for a completed/skipped job.
- Listing is newest ID first. Positive limits apply offset; nonpositive limits
  ignore offset. Empty job/document slices retain Go's JSON `null` shape.
- Go's SQLite DATETIME-to-string scan produces RFC3339 job timestamps, not raw
  SQLite timestamp text. Immediate enqueue return values have empty timestamps.
  `ByID` deliberately omits mail provenance while hash/list readers return it.

The HTTP worker queue in `internal/selfhost/jobs.go` is **a separate JSON-file
store**, with worker ownership and actual lease expiry. Its existing Rust
implementation is in `symdesk-protocol`; it is not replaced by this SQL queue.
`internal/ingest/api.go` wraps store listing/retry; `pipeline.go` enqueues and
claims exact IDs, records duplicate skips, and completes/fails jobs. Wiring
those OCR/provider/facade flows to this crate is outside this store slice.

## Executable evidence

`queue_port_test.go` calls the real Go store and migrations, overriding SQLite
clock functions only in an explicitly selected, isolated test process. The
75-step fixture records typed results, exact Go JSON result bytes, exact errors,
and every table's ordered rows after **each** operation, plus exact
`sqlite_master` SQL and source hashes. Clock advances pin the backoff boundary,
retry exhaustion and running-job non-expiry. Reopen/reset is exercised mid-run.
Synthetic vault roots are opaque keys. Only the legacy note's separator is
materialized per platform and canonicalized to `/` in recorded path fields.
No credentials, provider calls or operator databases are accessed.

```sh
# Immutable regeneration/comparison followed by Rust replay:
make ingest-queue-differential
# Deliberate fixture generation only:
make ingest-queue-fixtures-generate
```

The Make target accepts `INGEST_GO` and `CARGO` command overrides. In delegated
worktrees both must point to the approved compiler launcher. Direct equivalent:
`PORT_GENERATE=1 go test ./internal/ingest/internal/store -count=1 -run
'^TestPortIngestQueueContract$' -args -port-ingest-queue`; omit `PORT_GENERATE`
for immutable check mode. Do not pass the oracle flag to the whole Go suite:
its test-only SQLite clock registration is intentionally process-global.

Rust adds independent-connection claim exclusion and nonempty migration-13 to
migration-14 upgrade tests. These supplemental tests are not a substitute for
the Go-generated state-machine fixture. Native Linux/Windows evidence and
coordinator provenance refresh remain integration gates. Unrelated classification,
Paperless, extraction and mail-poll API implementations are not included.
