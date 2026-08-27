# Contributing to SymDesk

Thanks for helping improve SymDesk. Contributions should preserve the project’s local-first, standalone-first design and keep the Markdown vault contract stable.

## Before opening a pull request

- Explain the user-visible problem and the smallest useful change.
- Keep public and Pro boundaries intact; do not add cloud, billing, or tenant-management code to this repository.
- Preserve CGO-free Go builds and zero stdout pollution for the MCP server.
- Update tests and documentation when behavior or contracts change.

## Local checks

```sh
make build
make lint
make test
```

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
