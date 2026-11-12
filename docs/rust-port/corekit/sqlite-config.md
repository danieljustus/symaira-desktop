# CoreKit `sqlitekit` / `configkit` adoption (CK-SQLITE)

Scope: every place the removed Go backend (last Go commit `36aad3d4`) called
CoreKit `sqlitekit` or `configkit`, and what the Rust successor does at
CoreKit revision `592bebb54080d6b37372e47c461cfa3aeca7d2af` (rusqlite 0.41.1).
`symaira-core-sqlite` and `symaira-core-config` are workspace dependencies.
Adoption rule: persisted bytes (schema objects, `schema_migrations` rows,
pragmas, file modes, `-wal`/`-shm` behavior) and config path/error output stay
identical to the frozen Go-recorded fixtures; otherwise the code stays local.

## Adopted

| Go site (36aad3d4) | Rust site | CoreKit API |
|---|---|---|
| `internal/sidecar/db.go` `sqlitekit.Open` | `symdesk-index` `Sidecar::open` | `open_with_existing_parent` (already adopted; now via `open_existing_parent`) |
| `internal/sidecar/db.go` `sqlitekit.Migrate` | `symdesk-index/src/lib.rs` `migrate` | `migrate` over an embedded `MigrationSource` |
| `internal/retrieval/internal/db/db.go` `sqlitekit.Open` | `symdesk-index/src/retrieval.rs` `RetrievalDb::open_at` | `open_with_existing_parent` (was `Connection::open` plus a hand-written pragma batch) |
| `internal/retrieval/internal/db/migrate.go` `sqlitekit.Migrate` | `symdesk-index/src/retrieval.rs` `migrate_retrieval` | `migrate` |
| `internal/retrieval/maintenance.go` `sqlitekit.Open(source)` (legacy migration and `RelocateIndex`) | `symdesk-index/src/retrieval_config.rs` (both sites) | `open_with_existing_parent` |
| `internal/ingest/internal/store/store.go` `sqlitekit.Migrate` | `symdesk-ingest/src/lib.rs` `Store::from_connection` | `migrate` |
| `internal/sidecar` test helper (`sidecar-rust-helper`) | `symdesk-index/src/bin/sidecar-rust-helper.rs` `open_raw_connection` | `open_with_existing_parent` |
| `internal/config/config.go` `GlobalPath` (`configkit.DefaultPath("symdesk")`) | `symdesk-core` `config::global_path` | `default_path_for_roots` (already adopted) |
| `internal/config/config.go` `MailConfigPath` and `internal/ingest/internal/config/mail_contract.go` `ConfigPath` (`configkit.DefaultPath("symingest")`) | `symdesk-core` `config::default_config_file`, used by `symdesk-cli` `mail_rules_cli::{config_path, global_config_path}` and `doctor_cli::mail_config_path` | `default_path_for_roots` |

Notes on the migration adoption (Go `sqlitekit.Migrate` equivalent, CoreKit
`lib.rs` `migrate`):

* The embedded `(version, sql)` lists become `<version>.sql` entries. CoreKit
  sorts by entry name, Go sorted the `embed.FS` listing; unit tests
  (`corekit_migration_order_tests`, `embedded_migrations_are_in_corekit_application_order`)
  pin that the declared order already equals the sorted order, so applied order
  and recorded `schema_migrations.version` rows are unchanged.
* `schema_migrations` DDL is byte-identical to the previous local text
  (`version TEXT PRIMARY KEY, applied_at DATETIME DEFAULT CURRENT_TIMESTAMP`).
* Added behavior: lock-contended statements retry against a five-second
  wall-clock budget (CoreKit `retry_locked`); migration failures now read
  `failed to execute migration <version>: ...` like Go's `sqlitekit.Migrate`
  instead of the bare SQLite message.
* `Store::open` and the `Sidecar`/`RetrievalDb` parent creation stay local
  (see below), so `0700` parent handling is unchanged.

## Kept local

| Site | Reason | CoreKit ref |
|---|---|---|
| `symdesk-ingest` `Store::open` parent creation, `symdesk-index` `create_parent_dir`/`create_database_file` | CoreKit `open` calls `safe_mkdir_all`, which opens every ancestor with `O_NOFOLLOW` and rejects a symlinked, foreign-owned or group/other-writable ancestor (`symaira-core-fs` `lib.rs:182-231`, `is_secure_handle` `:519`). Go used `os.MkdirAll(dir, 0700)` and accepted all of those. The retrieval database file is additionally pre-created `0600` before SQLite opens it. Adopting `open` would change accepted paths and the new-file mode. | `symaira-core-sqlite/src/lib.rs:71-93` |
| `symdesk-index` `MigrationSource` struct and `symdesk-ingest` copy | Two 20-line adapters over `include_str!` lists; no shared crate is in this packet's ownership. | `symaira-core-sqlite/src/lib.rs:216-273` |
| `symdesk-contacts` `open`, `migrate`, `open_memory` | Go relate store did not use `sqlitekit`: it sets `synchronous=NORMAL`, uses an `INTEGER` version plus `name` and `applied_at` ISO-8601 `schema_migrations` schema and a comment-stripping statement splitter. CoreKit `migrate` writes `TEXT` versions with `DATETIME` timestamps, so persisted bytes would differ. | `symaira-core-sqlite/src/lib.rs:253` (`SCHEMA`), `:273` (`migrate`) |
| `symdesk-core` `pre_rust_backup`, `symdesk-cli` `index_cli` read-only/backup opens | Not Go `sqlitekit` sites: read-only flags or ad hoc snapshots, outside the packet's write paths. `index_cli::backup_index` mirrors the Go `snapshotIndexFile` (`sqlitekit.Open(source)`) but lives in the CLI crate. | `symaira-core-sqlite/src/lib.rs:93-101` |
| Tests and fixtures opening raw `rusqlite::Connection` (for example `symdesk-index/tests/retrieval_*.rs`, `symdesk-cli/tests/support/*_state.rs`) | Inspection views of Go-created state must not apply WAL/foreign-key pragmas or change the file. | n/a |
| `symdesk-cli` `config_paths::load_ingest_config_inner` | Go `internal/ingest/internal/config` calls `configkit.NewLoader`; Rust deliberately uses the legacy `$HOME/.config/symingest/config.toml` location (ignores `XDG_CONFIG_HOME`) and Go-spelled error text. CoreKit `Loader` uses `default_path`, caches, and returns different messages (`global config error: failed to ...`). | `symaira-core-config/src/lib.rs:165-211` (`load`, `load_once`) |
| `symdesk-core` `config::load`, `apply_environment`, `Config` schema | Product allowlisted env overrides, validation and `failed to decode config file:` text; CoreKit derives env names from the schema and merges global/project files. Documented in `corekit-adoption.md` "Config slice". | `symaira-core-config/src/lib.rs:216-264` |
| Room config readers `symroom-cli/src/member_cli.rs:256` (`default_identity`) and `run_cli.rs:157` (`configured_approval_ttl`), Go `internal/room/config` (`UseLegacyConfigPath: true`) | They read the legacy `$HOME/.config/symroom/config.toml`, then `.symroom.toml`, then `SYMROOM_*`. CoreKit has no public legacy-path helper (the legacy path is computed inline in `load_once`, `lib.rs:193-197`); `default_path` would honor `XDG_CONFIG_HOME`, which Go's `UseLegacyConfigPath` ignored. The error text is product-specific. Also outside this packet's write paths (`symroom-cli`). | `symaira-core-config/src/lib.rs:91-109, 188-211` |
| PDF config loader (Go `internal/pdf/internal/config`, `symprint`, `UseLegacyConfigPath: true`) | No Rust loader exists: a search for `SYMPRINT_`/`symprint` config reads in `crates/*/src` finds only `symdesk-render` text, no TOML/env loading. Nothing to route. | n/a |
| `symdesk-storage` | Contains only the Postgres backend and DSN parsing; there is no SQLite code to migrate. | n/a |

## Upstream gaps and local follow-ups

1. **CoreKit `open` vs Go `MkdirAll(0700)`**: `symroom-core` already calls
   `symaira_core_sqlite::open` for the room index (Go: `MkdirAll` + `sqlitekit.Open`).
   `open` rejects symlinked, foreign-owned or group/other-writable ancestors
   (`symaira-core-fs/src/lib.rs:182-231`), which Go accepted. No frozen fixture
   exercises such a parent, so this is recorded rather than changed here.
2. **`symdesk-protocol/src/server_state.rs:49`** (successor of Go
   `internal/storage/sqlite.go` `OpenSQLite`, which used `sqlitekit.Open`) opens
   `.symdesk/server/state.db` with plain `Connection::open`: no WAL, no
   foreign keys, no busy timeout. Go's file was in WAL mode. The crate is outside
   this packet's write paths; the follow-up is to route it through
   `open_with_existing_parent` and re-check the HTTP replay
   (`http_replay.rs:803` excludes `state.db` from comparison).
3. **No public legacy config path**: `Options::use_legacy_config_path` is only
   reachable through `Loader::load`; the helper that computes it is private
   (`lib.rs:193-197`). Desktop's Go-legacy readers (`symingest`, `symroom`,
   `symprint`) therefore cannot reuse it without adopting the whole `Loader`.
4. **Uncleaned `default_path_for_roots`**: CoreKit joins without `filepath.Clean`
   (`lib.rs:98-109`), unlike Go's `filepath.Join`. The desktop adapter
   (`config::default_config_file`) keeps the lexical cleanup and Windows
   verbatim handling locally.

## Checks

See the commit message and the packet report for the executed commands and
counts: `cargo fmt --all --check`, `cargo clippy -p symdesk-index -p
symdesk-ingest -p symdesk-core -p symdesk-cli --all-targets --all-features
--locked -- -D warnings`, `cargo test` for the same crates,
`make cutover-state-differential`, `make sidecar-metadata-differential`,
`make config-save-differential config-precedence-differential`. The full
workspace gate is pending with the coordinator.
