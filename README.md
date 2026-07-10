# symaira-desktop (`symdesk`)

[![CI](https://github.com/danieljustus/symaira-desktop/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/danieljustus/symaira-desktop/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/danieljustus/symaira-desktop)](https://github.com/danieljustus/symaira-desktop/releases) [![License](https://img.shields.io/github/license/danieljustus/symaira-desktop)](LICENSE)

The composition **shell** of the [Symaira](https://github.com/danieljustus?tab=repositories&q=symaira) ecosystem.

A local-first, **agent-native** workspace that unifies your documents, notes, knowledge and AI over a single plain-Markdown vault — by **composing the existing Symaira tools** rather than reimplementing them. It is the realization of the "Paperless + Obsidian + Notion AI" dream as *one surface*, deliberately **not** a monolith.

`symdesk` is a Go core that is simultaneously a **CLI** and an **MCP server**, paired with a **native SwiftUI macOS app** (`SymDesk.app`) built on [`symaira-appkit`](https://github.com/danieljustus/symaira-appkit). The same service layer is used by you, by the app, and by AI agents — operating on one Markdown vault through identical contracts.

- **Owns:** the Markdown-as-SSOT vault contract, one SQLite sidecar index, the native app, and the runtime composition layer
- **Composes at runtime today:** [`symaira-ingest`](https://github.com/danieljustus/symaira-ingest) (OCR/ingest), [`symseek`](https://github.com/danieljustus/symaira-seek) (search), [`symmemory`](https://github.com/danieljustus/symaira-memory) (RAG/graph), [`symfetch`](https://github.com/danieljustus/symaira-fetch) (web clipping), and [`symvault`](https://github.com/danieljustus/symaira-vault) (secrets resolution), all detected dynamically via `PATH` probe (the core never depends on their presence and degrades gracefully).
- **Delegates (does not rebuild):** spreadsheets → LibreOffice; code editing → editor plugins + agent orchestration; Obsidian Canvas/whiteboards → Obsidian; iCloud sync → the OS
- **Language:** Go (CGO-free, core) + Swift/SwiftUI (app). **License:** Apache-2.0

![SymDesk architecture](assets/symdesk-architecture.svg)

## Why SymDesk

- One plain-Markdown vault remains the source of truth instead of locking data into a proprietary database.
- The CLI, MCP server, and native macOS app share the same service layer and contracts.
- Symaira tools are composed at runtime, so optional capabilities degrade gracefully when a companion tool is unavailable.
- Local-first operation keeps documents, indexes, and configuration under the user’s control.

## Status

Working MVP: Go core (`symdesk`) with CLI + stdio MCP server, SQLite sidecar index (FTS5), vault contract v2 ([VAULT.md](VAULT.md)), and the native SwiftUI app with editor, command palette, backlinks, graph, saved views, drag-&-drop ingest and AI dock — plus a document workspace: document grid with quick filters, PDF/text viewer with a details inspector, first-run onboarding, and a Discover tab.

## Installation

### CLI from GitHub Releases

Download the archive for your platform from the [latest GitHub Release](https://github.com/danieljustus/symaira-desktop/releases/latest), extract it, and place `symdesk` on your `PATH`.

### CLI from source

```sh
go install github.com/danieljustus/symaira-desktop/cmd/symdesk@latest
```

### Native macOS app from source

The SwiftUI app currently builds from source on macOS 14 or newer:

```sh
brew install xcodegen
xcodegen generate
xcodebuild build -project SymDesk.xcodeproj -scheme SymDesk -destination 'platform=macOS'
```

## Development

```sh
make build          # → bin/symdesk
make test           # go test -race ./...

### macOS app (requires xcodegen)
xcodegen generate
xcodebuild build -project SymDesk.xcodeproj -scheme SymDesk -destination 'platform=macOS'
```

## CLI sample

```text
$ symdesk version --json
{"tool":"symdesk","version":"0.5.1","schema_version":1}
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
symdesk recipe validate .symdesk/recipes/daily.yml  # validate automation without running it
symdesk recipe run .symdesk/recipes/daily.yml       # stage runner proposals for review
symdesk recipe diff <run-id>                         # inspect proposed files
symdesk recipe accept <run-id>                       # apply an approved proposal
symdesk mcp                    # stdio MCP server for agents
symdesk doctor                 # health check
symdesk version --json         # {"tool":"symdesk","version":...,"schema_version":1}

## Document workflow (vault contract v2)
symdesk docs list --type invoice          # list indexed documents, with filters
symdesk docs review                       # list documents needing review (low-confidence / missing metadata)
symdesk doc status <file> paid            # set document status (open|paid|submitted|done|...)
symdesk doc due <file> 2026-12-31        # set document due date (ISO-8601)
symdesk similar <file>                    # find near-duplicate documents by SimHash
symdesk demo init [dir]                   # materialise the built-in demo vault
```

All commands support `--json` for machine-readable output.

### Reviewed recipes (optional)

Recipes live in `.symdesk/recipes/*.yml`. They declare an allowed trigger, an
explicit tool allow-list, and a hard `write_cap`. `symdesk` delegates execution
to an optional `symvibe` runtime through a versioned JSON request/response
contract; the runtime can only propose file contents. Each run is retained in
`.symdesk/runs/<id>/` as JSON plus a readable Markdown trace. No proposal
changes the vault until `symdesk recipe accept <id>` is invoked; rejected runs
remain available for inspection.

### AI (optional, local-first or cloud)

`symdesk ask` and `symdesk transform` support both local models via Ollama and cloud providers (Anthropic Claude). If no provider is configured, the system degrades honestly by explaining the missing configuration and returning search results.

#### Local AI (Ollama)

To use a local model, configure the Ollama endpoint and model name:

```sh
export SYMDESK_LLM_PROVIDER=ollama      # default
export SYMDESK_OLLAMA_URL=http://localhost:11434
export SYMDESK_OLLAMA_MODEL=llama3.2     # optional, default llama3.2
```

#### Cloud AI (Anthropic Claude)

To use Anthropic's Claude models, set the provider, model, and API key:

```sh
export SYMDESK_LLM_PROVIDER=anthropic
export SYMDESK_LLM_MODEL=claude-3-5-sonnet-20240620  # optional, default claude-3-5-sonnet-20240620
```

The API key (`SYMDESK_LLM_API_KEY`) is resolved dynamically in priority order:

1. **Environment Variable**: Directly set `SYMDESK_LLM_API_KEY`.
2. **Symvault (1Password)**: If `symvault` is installed on your `PATH`, you can set the key to a `symvault` secret reference:
   ```sh
   export SYMDESK_LLM_API_KEY="op://vault/item/llm-api-key"
   ```
3. **macOS Keychain Fallback**: If no key is set in the environment, `symdesk` checks the macOS Keychain for a password under service `symaira-desktop` and account `llm-api-key`. You can store it using:
   ```sh
   security add-generic-password -s symaira-desktop -a llm-api-key -w "your-api-key-here"
   ```
