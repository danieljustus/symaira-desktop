# symaira-desktop (`symdesk`)

The composition **shell** of the [Symaira](https://github.com/danieljustus?tab=repositories&q=symaira) ecosystem.

A local-first, **agent-native** workspace that unifies your documents, notes, knowledge and AI over a single plain-Markdown vault — by **composing the existing Symaira tools** rather than reimplementing them. It is the realization of the "Paperless + Obsidian + Notion AI" dream as *one surface*, deliberately **not** a monolith.

`symdesk` is a Go core that is simultaneously a **CLI** and an **MCP server**, paired with a **native SwiftUI macOS app** (`SymDesk.app`) built on [`symaira-appkit`](https://github.com/danieljustus/symaira-appkit). The same service layer is used by you, by the app, and by AI agents — operating on one Markdown vault through identical contracts.

- **Owns:** the Markdown-as-SSOT vault contract, one SQLite sidecar index, the native app, and the runtime composition layer
- **Composes at runtime:** [`symseek`](https://github.com/danieljustus/symaira-seek) (search), [`symmemory`](https://github.com/danieljustus/symaira-memory) (RAG/graph), [`symfetch`](https://github.com/danieljustus/symaira-fetch) (web→md), [`symvault`](https://github.com/danieljustus/symaira-vault) (secrets), [`symaira-ingest`](https://github.com/danieljustus/symaira-ingest) (OCR)
- **Delegates (does not rebuild):** spreadsheets → LibreOffice; code editing → editor plugins + agent orchestration; iCloud sync → the OS
- **Language:** Go (CGO-free, core) + Swift/SwiftUI (app). **License:** Apache-2.0

## Status

Working MVP: Go core (`symdesk`) with CLI + stdio MCP server, SQLite sidecar index (FTS5), vault contract v1 ([VAULT.md](VAULT.md)), and the native SwiftUI app with editor, command palette, backlinks, graph, saved views, drag-&-drop ingest and AI dock.

## Build

```sh
make build          # → bin/symdesk
make test           # go test -race ./...

# macOS app (requires xcodegen)
xcodegen generate
xcodebuild build -project SymDesk.xcodeproj -scheme SymDesk -destination 'platform=macOS'
```

## Usage

```sh
export SYMDESK_VAULT=~/Vault   # or configure via config file / --vault

symdesk index                  # index the vault into the sidecar
symdesk ls                     # list notes
symdesk search "invoice 2026"  # FTS5 full-text search
symdesk note new "My Note"     # create a note (frontmatter per vault contract)
symdesk props <file>           # frontmatter properties
symdesk backlinks <file>       # incoming wikilinks
symdesk graph                  # nodes + edges for the link graph
symdesk views ...              # saved database views
symdesk ingest <file>          # copy a document into inbox/ + create a stub note
symdesk ask "question?"        # AI answer grounded in vault search results
symdesk events --json          # NDJSON change stream (used by the app)
symdesk mcp                    # stdio MCP server for agents
symdesk doctor                 # health check
symdesk version --json         # {"tool":"symdesk","version":...,"schema_version":1}
```

All commands support `--json` for machine-readable output.

### AI (optional, local-first)

`symdesk ask` uses a local [Ollama](https://ollama.com) instance when configured — otherwise it degrades honestly and returns the top search results:

```sh
export SYMDESK_OLLAMA_URL=http://localhost:11434
export SYMDESK_OLLAMA_MODEL=llama3.2   # optional, default llama3.2
```
