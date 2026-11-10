# Contributing to SymDesk

Thanks for helping improve SymDesk. Contributions should preserve the project’s local-first, standalone-first design and keep the Markdown vault contract stable.

## Before opening a pull request

- Explain the user-visible problem and the smallest useful change.
- Keep public and Pro boundaries intact; do not add cloud, billing, or tenant-management code to this repository.
- Preserve zero stdout pollution for the MCP server and the frozen Go-recorded contracts.
- Update tests and documentation when behavior or contracts change.

## Local checks

The core is Rust (toolchain pinned in `rust-toolchain.toml`); no Go toolchain is needed. The Go implementation was retired in RUST-019: its last commit is `36aad3d4` (tag `v0.12.2`), where the Go sources stay readable in git history.

```sh
make build
make lint
make test
make port-contract # fixture checksums + frozen replay of the Go-recorded fixtures
```

Never edit committed fixtures under `testdata/port/` by hand or regenerate them: the Go generators are gone, so `scripts/rust-port/provenance_check.py` pins every fixture byte.

For macOS app changes, also run:

```sh
xcodegen generate
xcodebuild build -project SymDesk.xcodeproj -scheme SymDesk -destination 'platform=macOS'
xcodebuild test -project SymDesk.xcodeproj -scheme SymDeskCoreTests -destination 'platform=macOS'
```

## Pull requests

Use a focused branch and describe the change, verification performed, and any compatibility or migration notes. Keep commits small enough to review. Pull requests should be ready for CI and should not include credentials, generated build output, local vault data, or audit artifacts.

## Reporting security issues

Do not open a public issue for a suspected vulnerability. Follow [SECURITY.md](.github/SECURITY.md) instead.
