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
