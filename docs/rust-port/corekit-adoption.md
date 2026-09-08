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
