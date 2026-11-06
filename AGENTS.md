# symaira-desktop — Agent Instructions

## Current product contract

[PB-2026-09-09](docs/product-boundaries.md) keeps Desktop the document/knowledge-work product, usable without Brain. Brain owns optional Browse/Operate and the future credential-management UI; Desktop does not absorb them or become a generic launcher. Preserve direct optional web-worker/credential-service access without requiring Brain memory or a gateway profile. Documents remain authoritative here; only explicit provenance-bearing context is promoted to Brain. Existing runtime contracts remain until verified migrations.

Local-first, self-hostable, agent-native workspace over a single plain-Markdown vault. `symdesk` (Rust core) runs as CLI, stdio MCP server, authenticated self-hosted HTTP document API, or distributed OCR worker. Native SwiftUI apps for macOS (`SymDeskApp`) and iOS (`SymDeskMobile`) open a local/iCloud vault directly or connect to the self-hosted server.

Identity: **Markdown-vault workspace product, and the human shell of the ecosystem.** The separate hub app thesis was abandoned and `symaira-hub` archived on 2026-08-22 (`docs/repo-konsolidierung.md` §7 in the workspace); this is where a person works, while agents talk to the MCP servers directly. A **server-rendered browser UI** (server-side templates + vanilla JS, embedded in the `symdesk` binary) is accepted in principle; see `docs/BROWSER-ACCESS.md`. The HTTP API also serves JSON for native and machine clients.

## Commands

```bash
# Rust core (CLI/MCP/API/worker) — no Go toolchain anywhere (RUST-019)
make build              # → bin/symdesk, bin/symroom (cargo build --release --locked)
make test               # cargo nextest + doc tests for the whole workspace
make lint               # cargo fmt --check, clippy -D warnings, release-signing contract
make rust-gates         # check, lint, test, features, coverage, audit/deny, unsafe inventory, version contract
make port-contract      # Go-free: fixture checksums + frozen replay of every Go-recorded fixture
make port-contract-frozen  # the frozen replay alone, with `go` removed from PATH
make release-contract   # Rust release archive + container smoke contract inputs
make docker-build       # self-host container

# Frozen Go-recorded fixtures: never edit or regenerate by hand (the Go generators are gone).
python3 scripts/rust-port/provenance_check.py [--history]   # --history needs full git history

# Local gate (macOS native / Linux arm64 container)
scripts/rust-port/local-gate.sh macos|linux|all [make-target ...]

# macOS app (XcodeGen required: brew install xcodegen)
xcodegen generate
xcodebuild build -project SymDesk.xcodeproj -scheme SymDesk -destination 'platform=macOS'

# iOS app: open SymDesk.xcodeproj in Xcode, target SymDeskMobile (iOS 18+)

# Release: scripts/release/build.sh → Linux amd64/arm64 (musl) + macOS arm64 archives (.github/workflows/release.yml). Windows and macOS Intel are no longer required (platform scope decision 2026-10-29, #1364).
```

Entry points: `crates/symdesk-cli` (`symdesk`) and `crates/symroom-cli` (`symroom`). `symdesk --help` groups the subcommands into Vault, Document, AI, Server and Maintenance families; run it rather than trusting a list here, which drifts with every added command.

**Go history.** The Go implementation (CLI, MCP, API, generators, GoReleaser) was removed in RUST-019. The last commit containing Go is `075fdf94` (tag `v0.12.2`); the sources stay readable there (`git show 075fdf94:cmd/symdesk/main.go`). The committed fixtures under `testdata/port/` and `crates/*/tests/data/` are Go-recorded and byte-frozen; `testdata/port/provenance.json` pins their checksums and the oracle identity.

## Structure

```
crates/                 # Rust workspace (see Cargo.toml)
  symdesk-cli/          #   `symdesk` binary: CLI, stdio MCP server, HTTP API, OCR worker
  symroom-cli/          #   `symroom` binary (signed project journal)
  symdesk-core/         #   config, paths, shared contracts
  symdesk-vault/        #   vault contract — Markdown files are the SSOT; history, retention, permissions
  symdesk-index/        #   SQLite/FTS5 sidecar + hybrid retrieval (migrations/) — derived, rebuildable
  symdesk-ingest/       #   OCR/ingest pipeline, mail, Paperless/Notion import (migrations/)
  symdesk-render/       #   Markdown → PDF, SymDraw, archive, PDF ops (assets/ fonts, Typst templates)
  symdesk-contacts/     #   contact references (migrations/)
  symdesk-service/ symdesk-protocol/ symdesk-storage/   # service layer, protocol types, self-host storage (incl. PostgreSQL)
  symroom-core/         #   room journal, identity, events
  test-support/         #   shared test helpers
fuzz/                   # cargo-fuzz targets
testdata/port/          # Go-recorded, byte-frozen fixtures (+ provenance.json)
scripts/rust-port/      # frozen-contract runner, provenance check, replay harnesses, local gate
scripts/release/        # Rust release packaging
room/client/            # Swift Project Journal UI module, embedded in the SymDesk app target
meet/                   # nested Swift package, embedded in the SymDesk app target
Sources/
  SymDeskCore/   # Swift shared library bridging the core (consumes symaira-appkit, exact-pinned)
  SymDeskApp/    # macOS SwiftUI app
  SymDeskMobile/ # iOS SwiftUI app
Tests/           # 3 Swift test targets (XCTest — needs Xcode toolchain)
docs/            # ARCHITECTURE.md, PLAN.md, SELF_HOSTING.md, rust-port/
```

## Crate Boundaries (replaces the Go facade rules, issue #536)

The absorbed tools (`print`, `seek`, `relate`, `ingest`, `room`) are Rust crates in this workspace and are linked in-process; the former Go facades (`internal/pdf`, `internal/retrieval`, `internal/contacts`, `internal/ingest`) map to `symdesk-render`, `symdesk-index`, `symdesk-contacts`, `symdesk-ingest`.

- Depend on a crate only through its public API; never reach into another crate's private modules or copy its assets. Assets belong to the owning crate (`crates/symdesk-render/assets`, `crates/*/migrations`).
- `symdesk` is the single unified binary and human shell of the ecosystem; `symroom` exists only because the Project Journal surface bridges a separate binary (issue #541). `meet/` stays a standalone Swift menu-bar agent (not linked into the core).
- Do not add compile-time dependencies on **separately released** Symaira tools — those are found at runtime with graceful fallback.
- Enforced by the Cargo workspace layout, `cargo clippy -D warnings`, `cargo deny` and the unsafe inventory (`make rust-gates`).

## Conventions (repo-specific)

- **Markdown is the SSOT.** The SQLite sidecar (`crates/symdesk-index`) is a derived FTS5 index — never treat it as authoritative; it must stay rebuildable from the vault.
- **Pure-Rust core** with locked dependencies (`--locked`), static musl Linux release binaries; `deny(unsafe_code)` is the default (inventory in `scripts/rust-port/unsafe-allowlist.json`).
- **Zero stdio pollution** in `symdesk mcp` — stdout carries JSON-RPC frames only; logs to stderr.
- **Swift apps consume `symaira-appkit` exact-pinned** — do not reintroduce app-local Theme/Process-runner code; extend appkit (workspace rule, applies here too).
- **GUI work follows the Symaira Interface Guidelines** in that AppKit checkout: read `GUIDELINES.md`, then `DESIGN.md` and `docs/workspace-foundation.md`. Review `Sources/SymDeskApp`, `Sources/SymDeskMobile`, `ingest/client/Sources` and `room/client/Sources` with `python3 <symaira-appkit>/scripts/gui-review.py <root>` plus the manual checklist in GUIDELINES.md section 13 before a UI change lands.
- **XcodeGen**: `SymDesk.xcodeproj` is generated from `project.yml` — edit `project.yml`, never the `.xcodeproj`.
- **Frozen fixtures:** `testdata/port/**` is Go-recorded and byte-identical; do not normalize, hand-edit or regenerate it. `make port-contract` fails on any drift.
- **Self-host API is authenticated** — see `docs/SELF_HOSTING.md`; do not add unauthenticated endpoints.
- CI: `.github/workflows/{ci,codeql,container,release}.yml` (Rust + Swift only; no `setup-go`).

## Anti-Patterns (this repo)

- Do NOT add a separate SPA / TS / React frontend with its own build toolchain — the decided browser path is server-rendered HTML embedded in the Rust binary (`docs/BROWSER-ACCESS.md`).
- Do NOT rebuild a generic tool launcher here. Being the human shell means owning the vault workspace, not proxying every CLI: a tool that is not part of this product stays bound to the harness by the user.
- Do NOT add compile-time dependencies on **separately released** Symaira tools — those are found at runtime with graceful fallback. The absorbed tools are the opposite case: retrieval, PDF, contacts and ingest are linked directly as the crates `symdesk-index`, `symdesk-render`, `symdesk-contacts` and `symdesk-ingest`, never probed on `PATH`. Do NOT bypass their public APIs.
- Do NOT write vault state only to SQLite — Markdown files first, sidecar follows.
