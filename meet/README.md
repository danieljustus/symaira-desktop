# symaira-meet

> **Moved 2026-08-20** into `symaira-desktop/meet/` as part of the Symaira
> repo consolidation (27 → 13, `docs/repo-konsolidierung.md` §3.2). The code
> now lives in the symaira-desktop repository; the original
> `danieljustus/symaira-meet` repo remains active until the deep Go↔Swift
> integration is done.

[![CI](https://github.com/danieljustus/symaira-meet/actions/workflows/ci.yml/badge.svg)](https://github.com/danieljustus/symaira-meet/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/danieljustus/symaira-meet)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/danieljustus/symaira-meet)](https://github.com/danieljustus/symaira-meet/releases/latest)

![Symaira Meet social preview](docs/assets/social-preview.png)

`symmeet` is a local-first, standalone command-line tool for durable meeting
artifacts. It runs on macOS 15 or newer (Apple Silicon and Intel) and does not
require another Symaira binary, a cloud account, or telemetry.

> Current state: the first stable release ships local audio capture, transcription, model
> management, export formats, a menu-bar recording agent, and an MCP server.
> Meeting content stays on the device by default. Cloud transcription, accounts,
> automatic meeting detection, live captions, Intel Mac support, and encryption at
> rest are not included.

## Install

### Homebrew (recommended)

```bash
brew install danieljustus/tap/symmeet                 # CLI
brew install --cask danieljustus/tap/symmeet-agent    # menu-bar agent app
```

Prebuilt binaries (CLI `.tar.gz`, agent `.dmg`, checksums, SBOM) are attached
to every
[GitHub Release](https://github.com/danieljustus/symaira-meet/releases/latest).

### From source

Build from source requires Swift 6.0+ and macOS 15 or newer:

```bash
git clone https://github.com/danieljustus/symaira-meet.git
cd symaira-meet
swift build -c release
cp .build/release/symmeet /usr/local/bin/
```

## Build

```bash
swift build
swift test
make lint
.build/debug/symmeet --help
```

The binary is named `symmeet`. Every `--json` command writes exactly one
snake_case JSON document to stdout; diagnostics stay on stderr.

### Tests and coverage

`swift test` (and `make test`) requires a **full Xcode installation** — the
Command Line Tools package does not include XCTest. On CLT-only machines the
Makefile fails fast and points at the canonical gate instead: CI runs the test
suite with coverage on every push and pull request and uploads
`coverage-report` (`coverage.lcov`) as a workflow artifact (GitHub Actions →
CI → run → Artifacts). With full Xcode, `make coverage` produces the same
`coverage.lcov` locally.

## CLI

```text
symmeet version [--json]
symmeet version check [--force] [--output-json]
symmeet doctor [--json]
symmeet config path [--json]
symmeet meeting list [--json]
symmeet meeting show <meeting_id> [--json]
symmeet meeting trash <meeting_id> [--json]
symmeet meeting restore <meeting_id> [--json]
symmeet model list [--json]
symmeet model install <model> [--json]
symmeet model remove <model> [--json]
symmeet record [--json]
symmeet capture sources [--json]
symmeet permissions status [--json]
symmeet permissions request <mic|screen> [--json]
symmeet transcribe <file> [--json]
symmeet process <meeting_id> [--json]
symmeet diarize <meeting_id> [--json]
symmeet speaker list <meeting_id> [--json]
symmeet speaker label <meeting_id> ... [--json]
symmeet speaker merge <meeting_id> ... [--json]
symmeet speaker split <meeting_id> ... [--json]
symmeet speaker reset <meeting_id> [--json]
symmeet export <meeting_id> [--json]
symmeet job list [--json]
symmeet job show <job_id> [--json]
symmeet job cancel <job_id> [--json]
symmeet job retry <job_id> [--json]
symmeet mcp
symmeet capabilities [--json]
symmeet benchmark <file> [--json]
symmeet completion <bash|fish|zsh>
```

Exit codes: `0` success, `1` runtime failure, `2` invalid input or
configuration, `3` permission denied, and `4` unsupported operation. The
stable `version --json` handshake is:

```json
{"tool":"symmeet","version":"...","schema_version":1}
```

## Why symmeet

- Local-first: meeting content stays on the device by default.
- Standalone-first: artifacts are portable files, not records in a proprietary
  database.
- Contract-first: integrations use versioned, snake_case JSON at runtime.
- Privacy by design: future recording requires fresh, interactive authorization.

## Compared to cloud meeting tools

Cloud meeting tools (Otter, Fireflies, Granola, ...) transcribe in the vendor's
cloud; symmeet keeps the whole pipeline on your device.

| Axis | symmeet | Cloud meeting tools |
| --- | --- | --- |
| Local-first | Content stays on the device by default | Audio and transcripts are processed and stored in the vendor cloud |
| Standalone-first | Artifacts are portable files you keep and re-process | Records live in a proprietary, vendor-managed database |
| Account | None required | Account and subscription typically required |
| Telemetry | None by design | Usage data collected by default |
| Portability | Plain files with versioned, documented JSON contracts | Export is limited to the vendor's formats, when available |

## License

Apache-2.0. See [LICENSE](LICENSE).
