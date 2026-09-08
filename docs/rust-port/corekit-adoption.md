# CoreKit foundation adoption

## Version handshake slice

The staged Rust `symdesk` and `symroom` CLIs consume `symaira-core-version`
through `symdesk-core`, pinned to CoreKit commit
`d382b8615ce879bac6f23c7250e13667934e8f93` with `Cargo.lock` resolution.
The crate remains private (`0.0.0`, `publish = false` upstream); this is a Git
adoption, not a crates.io or product release.

The local `VersionDocument` duplicate is removed. CoreKit owns the payload
fields and plain-text formatting. Desktop retains its thin newline/JSON
serialization adapter and existing CLI parser behavior, including default
versions, inherited flags, extra arguments and error exit codes. This slice
does not switch to CoreKit's separate Go-escaping JSON writer or alter the
existing public rendering functions' signatures.

Verification commands:

```sh
cargo fmt --all --check
cargo clippy --workspace --all-targets --all-features --locked -- -D warnings
cargo nextest run --workspace --all-features --locked
cargo test --workspace --doc --all-features --locked
make rust-version-contract
cargo audit
cargo deny check
```

The initial local macOS run passed all 52 workspace tests and all 17 selected
Go/Rust version differential cases, using both built release binaries.
Native Linux/Windows evidence must come from CI on the adoption revision;
local success does not establish those platforms or product-release adoption.

No Go implementation, fallback, Swift bridge, HTTP value gate or migration
work-item completion status is changed. Subsequent foundation families need
separate behavior review; CoreKit exit categories must not replace the CLI's
existing numeric exit behavior by assumption.

## Exit code slice

The audit covered every `ExitCode` return path in `crates/symdesk-cli/src/main.rs`
and `crates/symroom-cli/src/main.rs`. `symdesk` uses only process codes 0 and 1:
success paths map to `symaira_core_exit::ExitCode::Ok` (0), and all existing
error paths map to `ExitCode::Generic` (1). The shared taxonomy is therefore
used only where its numeric value is identical; output, parser, and error
behavior remain unchanged.

`symroom` retains its existing literal process code 2 for missing, unknown, or
invalid subcommands (`crates/symroom-cli/src/main.rs:26`, `:37`, and `:56`).
That code is `symaira_core_exit::ExitCode::NoInput`, but this slice does not
reinterpret the established Rust CLI contract, so the call sites remain
literal 2. Its success and generic I/O/serialization failures adopt CoreKit
`Ok` (0) and `Generic` (1). CLI integration tests pin representative process
statuses for both binaries, and a focused unit test pins the complete CoreKit
0–10 taxonomy.

## Config slice

**Audit conclusion: adoption deferred.** `symdesk-core` has a product-specific
`Config` schema and validation (`crates/symdesk-core/src/config.rs:9-80,
151-250`), but its generic-looking helpers are not behavior-compatible with
CoreKit's `symaira-core-config` at the pinned revision. No Cargo dependency or
loading call-site change was made: routing through CoreKit would change the
observable contract frozen by `crates/symdesk-core/tests/config_contracts.rs`.

The exact CoreKit rows reviewed were CFG-001 through CFG-007 in
`symaira-corekit/docs/rust-port/contract-matrix.json:351-454`. The comparison is:

- **Default paths (CFG-001):** there is a partial duplicate. Desktop exposes
  data/config/cache home and directory helpers plus a global path
  (`config.rs:311-349`), while CoreKit exposes only the config-file path and
  legacy option (`symaira-core-config/src/lib.rs:90-113, 193-201`). Desktop's
  helpers return `String`, accept portable Windows-looking absolute paths on
  every host (`config.rs:392-394`), and fall back to `./...` when HOME is
  absent (`config.rs:385-390, 396-405`). CoreKit uses host-native
  `PathBuf::is_absolute` and platform-selected HOME/USERPROFILE, and its
  `Loader` returns `cannot determine home directory` when no home exists
  (`symaira-core-config/src/lib.rs:107-132, 188-191`). These paths and failure
  behavior are not identical.
- **Precedence (CFG-003):** the conceptual order overlaps, but the loaders do
  not. Desktop `load` consumes one caller-supplied optional TOML string and
  then its explicit environment snapshot (`config.rs:284-300`); it does not
  read a global file, project `.<name>.toml`, or cache. CoreKit always checks
  the global file, then the project file, then the process environment
  (`symaira-core-config/src/lib.rs:192-211`). CoreKit's `Loader` also caches
  its first result and provides reload/reset operations (CFG-007;
  `symaira-core-config/src/lib.rs:135-186`), whereas Desktop has no such
  state. Therefore the nominal defaults < global < project < env order cannot
  be adopted without adding files and changing public behavior.
- **Environment handling (CFG-004/005):** Desktop applies a fixed, product
  allowlist, ignores empty values, validates ranges before assignment, and
  intentionally ignores currently unsupported tagged variables
  (`config.rs:88-149, 361-376`). CoreKit derives `{PREFIX}_{FIELD}` and nested
  names from serialized schema fields, parses bool/int/float/array values, and
  returns typed parse errors (`symaira-core-config/src/lib.rs:253-293,
  382-496`). This is a product adapter seam, not an identical generic
  implementation.
- **TOML merge and errors (CFG-002/006):** Desktop deserializes the complete
  supplied document directly and wraps failures as
  `failed to decode config file: ...` (`config.rs:289-296`). CoreKit reads
  filesystem paths, skips only NotFound, checks map/type compatibility,
  merges only non-zero overlay values, and prefixes errors with the path and
  operation (`symaira-core-config/src/lib.rs:215-250`). Those bytes and merge
  semantics differ, including the fact that CoreKit can produce global/project
  read/parse/apply errors that Desktop cannot produce.

The existing generated config fixture and tests explicitly pin Desktop's
single-input/allowlist/path behavior (`crates/symdesk-core/tests/config_contracts.rs:104-170`),
so this is evidence for deferral rather than a missing implementation. A
future adoption would require a CoreKit-compatible adapter or a separately
reviewed CoreKit change, followed by regenerated Go differential evidence; it
must not silently replace these semantics.

## Log slice

Log adoption is not applicable yet. The desktop Rust workspace has no local
logging framework duplicate: the source inventory finds only diagnostic
`eprintln!` calls in `crates/symdesk-protocol/src/lib.rs:138`,
`crates/symdesk-index/src/bin/sidecar-rust-helper.rs:52`, and a test diagnostic
in `crates/symdesk-index/src/contract_tests.rs:591`. There is no `tracing`,
`log`, `env_logger`, `fern`, or `slog` implementation to remove or route
through CoreKit's `symaira-core-log`; the CoreKit LOG-001–LOG-004 family is
therefore outside this adoption slice.

## FS slice

**Audit conclusion: adoption deferred.** The desktop Rust workspace already
uses `cap-std` for capability-scoped reads, but neither usage is a behavior-
identical duplicate of CoreKit's `symaira-core-fs`. No Cargo dependency or
call-site change was made.

The reviewed CoreKit rows are FS-001 through FS-007 in CoreKit's
`docs/rust-port/contract-matrix.json`. CoreKit's `validate_path` is a lexical
relative-path validator and its secure operations provide atomic writes, safe
removal, directory creation, locking, and Unix `openat(O_NOFOLLOW)` confinement
(`rust/symaira-core-fs/src/lib.rs:52-78, 180-250, 257-373, 392-503, 813-864`
at the pinned CoreKit revision). The desktop call sites do not expose
those operations with the same observable contract:

- `symdesk-index` uses `cap_std::fs::Dir` to keep the vault root open while
  indexing and pruning, then reads metadata and bytes from the same opened
  handle (`crates/symdesk-index/src/lib.rs:352-424, 499-533`). Its generic-
  looking `storage_path` is also product-specific: it preserves Go's separate
  I/O and database-key path spellings, rejects non-UTF-8 keys, and delegates
  symlink confinement to the vault's canonicalization policy
  (`crates/symdesk-index/src/lib.rs:704-724`). Replacing this with CoreKit
  helpers would change index keys, error variants, or the no-read cache path.
  Directory creation for the sidecar additionally applies the product's
  `0700` policy to newly missing parents (`crates/symdesk-index/src/lib.rs:766-791`),
  not CoreKit's `safe_mkdir_all` API at an equivalent call site.
- `symdesk-protocol` opens the current vault root as a capability and uses
  handle-relative reads for HTTP file/range responses and snapshots
  (`crates/symdesk-protocol/src/lib.rs:398-437, 524-552, 578-606`). Its
  `confined_path` deliberately rejects backslashes, dot segments, empty
  segments, and `.symdesk` before the capability open
  (`crates/symdesk-protocol/src/lib.rs:639-667`). This is an HTTP wire-contract
  validator, not CoreKit's `validate_path`; its errors are translated to the
  fixed `400`/`404` responses at `:411-427`. The snapshot watcher also needs
  ordinary path-based create/write/rename/delete events in tests
  (`crates/symdesk-protocol/src/snapshot_cache.rs:327-352`), which are not
  generic CoreKit safe-tree operations.
- `symdesk-vault/src/paths.rs` is a separate secure-path resolver: it preserves
  the Go `filepath.Join` absolute-input behavior, canonicalizes existing and
  missing targets, and returns distinct traversal, vault, parent, path, and
  symlink-escape errors (`crates/symdesk-vault/src/paths.rs:10-48, 59-75`).
  CoreKit's lexical `validate_path` rejects absolute paths and returns
  `FsError::InvalidPath`, so adopting it would change both accepted paths and
  error mapping. The vault walker likewise intentionally reports symlink
  entries and sorts directory results (`crates/symdesk-vault/src/walk.rs:38-41,
  93-129`), rather than using CoreKit's mutation primitives.

There is therefore no genuine behavior-identical FS duplicate to remove in
this slice. Keep `cap-std` and the local path code until a future adoption
introduces an adapter whose path spelling, errors, permissions, and symlink
handling are proven byte/behavior-equivalent by the FS differential harness.

## SecretRef slice

SecretRef is not applicable to the desktop Rust workspace. An exact search of
all Rust sources under `crates/` found no `symvault://`, `secretref`, or
`SecretRef` call sites, so `symaira-core-secretref` was intentionally not
added. The SEC-001 CoreKit contract row remains outside this slice; the
references currently present in Go/Swift/docs are not Rust dependency call
sites to adopt.
