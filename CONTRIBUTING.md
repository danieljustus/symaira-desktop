# Contributing to SymDesk

Thanks for helping improve SymDesk. Contributions should preserve the project’s local-first, standalone-first design and keep the Markdown vault contract stable.

## Before opening a pull request

- Explain the user-visible problem and the smallest useful change.
- Keep public and Pro boundaries intact; do not add cloud, billing, or tenant-management code to this repository.
- Preserve CGO-free Go builds and zero stdout pollution for the MCP server.
- Update tests and documentation when behavior or contracts change.

## Local checks

The frozen Go/Rust oracle contracts require Go 1.26.9. Select that toolchain
explicitly rather than regenerating fixtures after a newer Go version changes
the recorded behavior.

```sh
export GOTOOLCHAIN=go1.26.9
make build
make lint
make test # macOS: CGO_ENABLED=0 go test -race ./...
```

On Linux and Windows, run `CGO_ENABLED=1 go test -race -count=1 ./...` with a
C toolchain instead of `make test`: the race detector requires CGO on those
platforms. This matches the CI test command; production builds stay CGO-free.

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
