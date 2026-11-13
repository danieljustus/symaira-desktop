# RUST-015 part PG: PostgreSQL self-host store (PG-001)

Worktree `r015-pg`, branch `local/r015-pg`, base `63f1189b`. Scope: contract row
PG-001 only; RUST-015's HTTP rows, workers and shares are other parts.

## What is ported

Go `internal/storage/postgres.go` (`PostgresBackend`, the `storage_entries`
key/value table behind the self-host server state) is ported as
`crates/symdesk-storage` (`PostgresBackend`, `Bounded`, `StorageError`, `Entry`).

- **Driver:** `tokio-postgres` 0.7.18 — pure Rust, SCRAM-SHA-256 included, no
  libpq and no native TLS library, so neither gate host needs a system
  PostgreSQL package. (`sqlx` was rejected: its PostgreSQL driver drags a macro
  and compile-time-checking stack in for four statements.) The workspace already
  runs `tokio`, which the driver shares.
- **Pool:** a purpose-built one (about 100 lines), not `deadpool`/`bb8`. The
  contract needs a deadline that also covers the wait for a connection, aborts
  the running statement server-side and discards that connection (what pgx does
  when its context ends), and `pool_max_conns` queueing identical to pgxpool.
  The same cancel-on-drop applies when a caller simply drops the future.
- **SQL:** the four statements and the DDL are byte-for-byte Go's, so server
  errors, the created table and its index are identical. Go's `[]byte`↔`bytea`
  *or* `text` conversion is kept (a table whose value column is `text` works).
- **Errors:** `StorageError` renders the Go texts where Go's are deterministic
  (`get storage key "k": ERROR: … (SQLSTATE 42703)`, `storage: key not found:
  "k"`, `storage: key must not be empty`, `closed pool`, …), including pgx's
  split between statement-preparation errors (`query storage keys with prefix
  …`) and execution errors surfaced while iterating (`iterate storage entries`).
- **DSN:** pgx URL and keyword/value forms through tokio-postgres' parser plus
  the pgxpool `pool_max_conns` parameter; `sslmode` verify-*/allow are mapped;
  invalid percent-encoding is rejected like pgx does.

## Go-recorded fixture

`scripts/rust-port/cmd/pggen` runs the **production** Go backend against a real,
disposable PostgreSQL 17 and records each step into
`testdata/port/storage/postgres.json` (27 scenarios, 1167 steps, registered in
`portgen` and `provenance.json`):

| Area | Scenarios |
|---|---|
| Schema creation / DDL | `schema_fresh` (columns, `collation`, PK constraint, index, object count, idempotent reopen) |
| CRUD | `crud_basic`, `value_semantics` (nil/empty/all 256 bytes/NUL/4 MiB), `reopen_state`, `close_semantics` |
| Ordering | `ordering_byte_collation` (`COLLATE "C"` under the image's `en_US.utf8`), `prefix_metacharacters`, `random_mix` (500 seeded operations), `bulk_query` (2000 rows, digests) |
| Errors | `invalid_keys` (empty key, NUL → 22021), `open_errors` (empty/garbage/bad-port/bad-percent DSN, 28P01, 3D000, refused port, required TLS), `role_privileges` (42501 on Open, read-only role), `key_size_limits` (54000) |
| Pooling | `pool_single_connection` (`pool_max_conns=1` queueing, queued op honors its deadline), `table_lock_deadline` (every op times out behind `ACCESS EXCLUSIVE`, pool recovers) |
| Concurrent transactions | `concurrent_same_key` (32 upserts), `concurrent_distinct_keys` (64+32), `row_lock_blocks_set`, `insert_conflict_waits`, `read_committed_visibility`, `open_race` (concurrent `CREATE TABLE IF NOT EXISTS`) |
| Upgrades / older shapes | `legacy_text_value`, `legacy_extra_columns`, `legacy_extra_not_null` (23502), `legacy_no_primary_key` (42P10), `legacy_renamed_column` (42703), `search_path_schema` |

Parity with SQLite mode is recorded, not assumed: the six sequential scenarios
also run through `OpenSQLite`; the fixture lists `sqlite_diffs`, which are exactly
the four NUL-character steps of `invalid_keys` (PostgreSQL rejects NUL in text,
SQLite accepts it). The generator aborts on any other difference.

Schema history: `internal/storage/postgres.go` has two commits (#311, #423) and
the table never changed, so "upgrades" are idempotent reopen plus the older or
foreign table shapes above. There is no migration code to port.

Determinism: three consecutive generations against one server are byte-identical.
Nondeterministic outcomes are recorded as invariants (`open_race` asserts at
least one Open wins and every loser fails with 23505/42P07/42710; the same-key
race checks the survivor is one of the written values and then settles it).

## Running it

```sh
make postgres-integration          # macOS or Linux host with docker
make postgres-fixtures-generate    # deliberate re-record (then re-freeze, see below)
```

`postgres-integration` starts its own container through
`scripts/rust-port/postgres-sidecar.sh` (`postgres:17@sha256:0af65001…`, the
image the existing CI sidecars run, pinned by digest; random `127.0.0.1` port,
per-run password, `tmpfs` data directory, removed on exit; a unique name — it
never touches other containers), then runs
`scripts/rust-port/postgres-integration.sh check`:

1. `pggen --check` — the Go store re-runs all scenarios against this server and must
   reproduce the committed bytes.
2. The Go store's own `-tags postgres` tests (`internal/storage`), which now derive
   their database from `SYMDESK_PG_ADMIN_DSN`; the script fails if they do not run.
3. `cargo test -p symdesk-storage --test postgres_contracts -- --ignored`: the Rust
   store replays every step and the final table state, plus a Rust-only test
   (server-killed connection is replaced; a dropped write is cancelled and never
   lands). An unset `SYMDESK_PG_ADMIN_DSN` fails the ignored tests; it cannot skip.

If `SYMDESK_PG_ADMIN_DSN` is already set (any disposable superuser DSN) the target
uses that server and starts nothing. The Linux gate container has no docker:
`local-gate.sh` starts the sidecar itself on a private network
(`symdesk-gate-pg-<slug>` / `symdesk-gate-net-<slug>`, no published port) for a
`postgres-integration` run and always removes both.

`make port-fixtures-check` has no server: its `pggen --check` form validates the
committed fixture's structure, oracle identity and checksum only; the live Go
replay is part of `make postgres-integration`.

Negative control: dropping `COLLATE "C"` from the Rust query makes 5 of the 27
scenarios fail (`crud_basic`, `ordering_byte_collation`, `prefix_metacharacters`,
`random_mix`, `legacy_text_value`).

## Declared limits

- **TLS is not implemented** (`sslmode=require|verify-*` fails to connect like Go
  against a server without TLS; a server that demands TLS is unreachable). A
  `rustls` connector over the workspace's `rustls` is the follow-up and should land
  before the Rust server defaults to this store for a remote database.
- Only `pool_max_conns` of pgxpool's `pool_*` DSN parameters is supported; the rest
  are rejected as DSN errors. pgx forwards unknown DSN keys to the server as runtime
  parameters; tokio-postgres rejects them at parse time.
- No `context.Context`: cancellation is by dropping the future or `bounded()`;
  Go's cancelled-before-call case has no Rust analogue. Keys are `&str`, so Go's
  invalid-UTF-8 keys cannot be expressed.
- A connection killed by the server is replaced transparently (Go hands that
  connection's error to the next caller); pinned by the Rust-only test, not the
  fixture, because Go's outcome there is timing-dependent.
- The Rust server (RUST-015 HTTP part) does not yet consume the store; wiring
  `ServerState` onto it belongs to that part.
- Hosted CI has no PostgreSQL job for this target yet; the evidence is local
  (macOS arm64 + Linux arm64). Native Windows is not claimed.

## Fixture identity

Adding `pggen`, the Makefile target, the Go test change and the `portgen`
registration changes the generator digest; re-frozen as
`testdata(port): re-freeze the generator digest (RUST-015 r015-pg)`. Any later
edit to a digest input (`scripts/rust-port/**/*.go`, `go.mod`, `go.sum`,
`Makefile`) needs the same re-freeze: commit, run `make port-fixtures-check`, copy
`checked=` into `generator_source_digest`.

## Verification (source commit `ef6968ee`)

`LOCAL_GATE_SLUG=r015-pg MAKE_PREFIX=/Users/daniel/.local/bin/dev-external scripts/rust-port/local-gate.sh all rust-check rust-lint rust-test port-contract postgres-integration`
on a clean tree (`dirty:false`):

| Host | Window (UTC) | Exit | Notes |
|---|---|---|---|
| macOS arm64 | 19:47:07–20:19:49 | 0 | nextest 969 passed, 5 skipped; `make postgres-integration` started its own container |
| Linux arm64 (native container) | 20:19:49–21:09:54 | 0 | nextest 970 passed, 5 skipped; `postgres-integration` ran against the docker-network sidecar `symdesk-gate-pg-r015-pg` |

Both hosts printed `PASS all fixtures and oracle provenance verified`,
`PASS postgres fixture live replay (27 scenarios against PostgreSQL 17)`,
`PASS Go store postgres-tagged integration tests` and
`replayed 27 scenarios / 1167 steps against PostgreSQL 17`. Receipts:
`target/local-gate/receipts.jsonl` (logs `macos-ef6968ee-194707.log`,
`linux-ef6968ee-201949.log`). Hosted CI and native Windows are not claimed.
