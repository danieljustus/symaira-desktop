# symaira-desktop (`symdesk`)

The composition **shell** of the [Symaira](https://github.com/danieljustus?tab=repositories&q=symaira) ecosystem.

A local-first, **agent-native** workspace that unifies your documents, notes, knowledge and AI over a single plain-Markdown vault — by **composing the existing Symaira tools** rather than reimplementing them. It is the realization of the "Paperless + Obsidian + Notion AI" dream as *one surface*, deliberately **not** a monolith.

`symdesk` is a Go core that is simultaneously a **CLI** and an **MCP server**, paired with a **native SwiftUI macOS app** (`SymDesk.app`) built on [`symaira-appkit`](https://github.com/danieljustus/symaira-appkit). The same service layer is used by you, by the app, and by AI agents — operating on one Markdown vault through identical contracts.

- **Owns:** the Markdown-as-SSOT vault contract, one SQLite sidecar index, the native app, and the runtime composition layer
- **Composes at runtime today:** [`symaira-ingest`](https://github.com/danieljustus/symaira-ingest) (OCR/ingest), [`symseek`](https://github.com/danieljustus/symaira-seek) (search), [`symmemory`](https://github.com/danieljustus/symaira-memory) (RAG/graph), [`symfetch`](https://github.com/danieljustus/symaira-fetch) (web clipping), and [`symvault`](https://github.com/danieljustus/symaira-vault) (secrets resolution), all detected dynamically via `PATH` probe (the core never depends on their presence and degrades gracefully).
- **Delegates (does not rebuild):** spreadsheets → LibreOffice; code editing → editor plugins + agent orchestration; iCloud sync → the OS
- **Language:** Go (CGO-free, core) + Swift/SwiftUI (app). **License:** Apache-2.0

## Status

Working MVP: Go core (`symdesk`) with CLI + stdio MCP server, SQLite sidecar index (FTS5), vault contract v2 ([VAULT.md](VAULT.md)), and the native SwiftUI app with editor, command palette, backlinks, graph, saved views, drag-&-drop ingest and AI dock — plus a document workspace: document grid with quick filters, PDF/text viewer with a details inspector, first-run onboarding, and a Discover tab.

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

# Document workflow (vault contract v2)
symdesk docs list --type invoice          # list indexed documents, with filters
symdesk docs review                       # list documents needing review (low-confidence / missing metadata)
symdesk doc status <file> paid            # set document status (open|paid|submitted|done|...)
symdesk doc due <file> 2026-12-31        # set document due date (ISO-8601)
symdesk similar <file>                    # find near-duplicate documents by SimHash
symdesk demo init [dir]                   # materialise the built-in demo vault
```

All commands support `--json` for machine-readable output.

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
