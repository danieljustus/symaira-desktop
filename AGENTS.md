# symaira-desktop — Agent Instructions

Local-first, self-hostable, agent-native workspace over a single plain-Markdown vault. `symdesk` (Go core) runs as CLI, stdio MCP server, authenticated self-hosted HTTP document API, or distributed OCR worker. Native SwiftUI apps for macOS (`SymDeskApp`) and iOS (`SymDeskMobile`) open a local/iCloud vault directly or connect to the self-hosted server.

Identity: **Markdown-vault workspace product, and the human shell of the ecosystem.** The separate hub app thesis was abandoned and `symaira-hub` archived on 2026-08-22 (`docs/repo-konsolidierung.md` §7 in the workspace); this is where a person works, while agents talk to the MCP servers directly. A **server-rendered browser UI** (Go templates + vanilla JS, embedded in the `symdesk` binary) is accepted in principle; see `docs/BROWSER-ACCESS.md`. The HTTP API also serves JSON for native and machine clients.

## Commands

```bash
# Go core (CLI/MCP/API/worker)
make build              # → bin/symdesk (go build ./cmd/symdesk)
make test               # CGO_ENABLED=0 go test -race ./...
make lint               # gofmt + go vet + corekit-guard + boundary-guard
make docker-build       # self-host container

# The nested modules are NOT covered by the root `./...` — Go does not
# descend into directories with their own go.mod. CI runs them as a
# `nested (<module>)` matrix job; locally:
for m in ingest print relate room seek; do (cd $m && go test ./...); done

# macOS app (XcodeGen required: brew install xcodegen)
xcodegen generate
xcodebuild build -project SymDesk.xcodeproj -scheme SymDesk -destination 'platform=macOS'

# iOS app: open SymDesk.xcodeproj in Xcode, target SymDeskMobile (iOS 18+)

# Release: GoReleaser → linux/darwin/windows × amd64/arm64 + Homebrew formula
```

Entry point: `cmd/symdesk/main.go`. `symdesk --help` groups the subcommands into Vault, Document, AI, Server and Maintenance families; run it rather than trusting a list here, which drifts with every added command.

## Structure

```
cmd/symdesk/     # Go CLI entry + all commands (main.go, commands.go, selfhost.go, ...)
internal/        # Go packages (see also the nested modules below):
  mcp/           #   stdio MCP server (server.go, handlers, tools)
  selfhost/      #   HTTP API server + OCR worker logic (incl. share.go expiring links)
  service/       #   core service layer (vault svc, meetings, views, relations, templates)
  vault/         #   vault contract — Markdown files are the SSOT
  sidecar/       #   SQLite/FTS5 sidecar index (//go:embed migrations) — derived, rebuildable
  ingest/        #   OCR ingest pipeline (Tesseract / Ollama)
  pdf/           #   in-process wrapper around print/api
  retrieval/     #   in-process wrapper around seek/api
  contacts/      #   in-process wrapper around relate/api
  testsupport/   #   TestMain isolation for the $HOME-backed stores
  mail/          #   IMAP mail ingestion
  permissions/   #   users, groups and document-level permissions (self-hosted server)
  retention/     #   automatic retention rules
  paperless/     #   Paperless-ngx export importer
  templatepath/  #   storage-path templating
  archive/       #   PDF/A archive generation
  ai/ compose/ config/ dbviews/ demo/ export/ history/ recipes/ searchquery/ secrets/ simhash/ watcher/
seek/ print/ relate/ room/ ingest/   # nested Go modules, each with its own go.mod
                 #   reached only through their public api/ package — internal/
                 #   is not importable across a module boundary
meet/            # nested Swift package, embedded in the SymDesk app target
Sources/
  SymDeskCore/   # Swift shared library bridging the Go core (consumes symaira-appkit, exact-pinned)
  SymDeskApp/    # macOS SwiftUI app
  SymDeskMobile/ # iOS SwiftUI app
Tests/           # 3 Swift test targets (XCTest — needs Xcode toolchain)
docs/            # ARCHITECTURE.md, PLAN.md, SELF_HOSTING.md
```

## Module Boundaries and Permitted Facades (issue #536)

The absorbed tools live in this repository as nested modules with their own `go.mod`. To preserve clear architectural separation and avoid tight coupling:

1. **Permitted in-process facades in `symaira-desktop` (`internal/`):**
   - `print/api` → wrapped exclusively by `internal/pdf` (and `internal/testsupport`)
   - `seek/api` → wrapped exclusively by `internal/retrieval` (and `internal/testsupport`)
   - `relate/api` → wrapped exclusively by `internal/contacts` (and `internal/testsupport`)
   - `ingest/api` → wrapped by `internal/ingest`, `internal/mail`, `internal/selfhost`, `internal/testsupport`, plus `cmd/symdesk` migration/doctor commands
   - `room/` → standalone `symroom` binary (entry point in `room/cmd/symroom`, not linked in Go core)
   - `meet/` → standalone Swift menu-bar agent (not linked in Go core)

2. **Boundary and replacement rules:**
   - Packages in `symaira-desktop` must NEVER import absorbed library internals (`.../internal/...`).
   - Packages outside the permitted facades must NEVER import absorbed modules directly; they must access functionality through the designated facade (e.g. use `internal/pdf` rather than importing `print/api`).
   - Root `go.mod` resolves absorbed modules via local `replace` directives pointing to `./print`, `./seek`, `./relate`, `./ingest`.
   - Absorbed modules (`print`, `seek`, `relate`, `ingest`) do NOT contain their own standalone `cmd/` entry points or duplicate tool launchers; `symdesk` is the single unified binary and human shell of the ecosystem.
   - Enforced automatically in `make lint` and CI via `scripts/check-module-boundaries.sh`.

## Conventions (repo-specific)

- **Markdown is the SSOT.** The SQLite sidecar (`internal/sidecar`) is a derived FTS5 index — never treat it as authoritative; it must stay rebuildable from the vault.
- **CGO-free Go core** (`CGO_ENABLED=0` in test/release paths) — cross-compilation + GoReleaser depend on it.
- **Zero stdio pollution** in `symdesk mcp` — stdout carries JSON-RPC frames only; logs to stderr.
- **Swift apps consume `symaira-appkit` exact-pinned** — do not reintroduce app-local Theme/Process-runner code; extend appkit (workspace rule, applies here too).
- **XcodeGen**: `SymDesk.xcodeproj` is generated from `project.yml` — edit `project.yml`, never the `.xcodeproj`.
- **Self-host API is authenticated** — see `docs/SELF_HOSTING.md`; do not add unauthenticated endpoints.
- CI: `.github/workflows/{ci,codeql,container,release}.yml`.

## Anti-Patterns (this repo)

- Do NOT add a separate SPA / TS / React frontend with its own build toolchain — the decided browser path is server-rendered HTML embedded in the Go binary (`docs/BROWSER-ACCESS.md`).
- Do NOT rebuild a generic tool launcher here. Being the human shell means owning the vault workspace, not proxying every CLI: a tool that is not part of this product stays bound to the harness by the user.
- Do NOT add compile-time imports of **separately released** Symaira tools — those are found at runtime with graceful fallback (`internal/compose`). The absorbed nested modules are the opposite case: `seek/`, `print/`, `relate/` and `ingest/` are linked directly through their `api/` packages via their permitted facades (`internal/retrieval`, `internal/pdf`, `internal/contacts`, `internal/ingest`), never probed on `PATH`. Do NOT bypass the facades or import absorbed module internals.
- Do NOT write vault state only to SQLite — Markdown files first, sidecar follows.
