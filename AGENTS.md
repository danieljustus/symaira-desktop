# symaira-desktop — Agent Instructions

Local-first, self-hostable, agent-native workspace over a single plain-Markdown vault. `symdesk` (Go core) runs as CLI, stdio MCP server, authenticated self-hosted HTTP document API, or distributed OCR worker. Native SwiftUI apps for macOS (`SymDeskApp`) and iOS (`SymDeskMobile`) open a local/iCloud vault directly or connect to the self-hosted server.

Identity: **Markdown-vault workspace product — NOT the tool hub** (hub is `symaira-hub`). A **server-rendered browser UI** (Go templates + vanilla JS, embedded in the `symdesk` binary) is accepted in principle; see `docs/BROWSER-ACCESS.md`. The HTTP API also serves JSON for native and machine clients.

## Commands

```bash
# Go core (CLI/MCP/API/worker)
make build              # → bin/symdesk (go build ./cmd/symdesk)
make test               # CGO_ENABLED=0 go test -race ./...
make lint               # gofmt -l . + go vet ./...
make docker-build       # self-host container

# macOS app (XcodeGen required: brew install xcodegen)
xcodegen generate
xcodebuild build -project SymDesk.xcodeproj -scheme SymDesk -destination 'platform=macOS'

# iOS app: open SymDesk.xcodeproj in Xcode, target SymDeskMobile (iOS 18+)

# Release: GoReleaser → linux/darwin/windows × amd64/arm64 + Homebrew formula
```

Entry point: `cmd/symdesk/main.go`. Subcommands: `version`, `mcp`, `serve`, `worker`, `mail`, `recipe`, `history`, `restore`, `trash`, `meeting`, `retention`, `permissions`, `paperless`, `events`, `doctor`.

## Structure

```
cmd/symdesk/     # Go CLI entry + all commands (main.go, commands.go, selfhost.go, ...)
internal/        # 24 Go packages:
  mcp/           #   stdio MCP server (server.go, handlers, tools)
  selfhost/      #   HTTP API server + OCR worker logic (incl. share.go expiring links)
  service/       #   core service layer (vault svc, meetings, views, relations, templates)
  vault/         #   vault contract — Markdown files are the SSOT
  sidecar/       #   SQLite/FTS5 sidecar index (//go:embed migrations) — derived, rebuildable
  ingest/        #   OCR ingest pipeline (Tesseract / Ollama)
  mail/          #   IMAP mail ingestion
  permissions/   #   users, groups and document-level permissions (self-hosted server)
  retention/     #   automatic retention rules
  paperless/     #   Paperless-ngx export importer
  templatepath/  #   storage-path templating
  archive/       #   PDF/A archive generation
  ai/ compose/ config/ dbviews/ demo/ export/ history/ recipes/ searchquery/ secrets/ simhash/ watcher/
Sources/
  SymDeskCore/   # Swift shared library bridging the Go core (consumes symaira-appkit, exact-pinned)
  SymDeskApp/    # macOS SwiftUI app
  SymDeskMobile/ # iOS SwiftUI app
Tests/           # 3 Swift test targets (XCTest — needs Xcode toolchain)
docs/            # ARCHITECTURE.md, PLAN.md, SELF_HOSTING.md
```

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
- Do NOT position desktop as the tool hub (that is symaira-hub's role).
- Do NOT add compile-time imports of sibling Symaira repos — runtime detection with graceful fallback.
- Do NOT write vault state only to SQLite — Markdown files first, sidecar follows.
