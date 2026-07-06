# symaira-desktop (`symdesk`)

The composition **shell** of the [Symaira](https://github.com/danieljustus?tab=repositories&q=symaira) ecosystem.

A local-first, **agent-native** workspace that unifies your documents, notes, knowledge and AI over a single plain-Markdown vault — by **composing the existing Symaira tools** rather than reimplementing them. It is the realization of the "Paperless + Obsidian + Notion AI" dream as *one surface*, deliberately **not** a monolith.

`symdesk` is a Go core that is simultaneously a **CLI** and an **MCP server**, paired with a **native SwiftUI macOS app** (`SymDesk.app`) built on [`symaira-appkit`](https://github.com/danieljustus/symaira-appkit). The same service layer is used by you, by the app, and by AI agents — operating on one Markdown vault through identical contracts.

- **Owns:** the Markdown-as-SSOT vault contract, one SQLite sidecar index, the native app, and the runtime composition layer
- **Composes at runtime:** [`symseek`](https://github.com/danieljustus/symaira-seek) (search), [`symmemory`](https://github.com/danieljustus/symaira-memory) (RAG/graph), [`symfetch`](https://github.com/danieljustus/symaira-fetch) (web→md), [`symvault`](https://github.com/danieljustus/symaira-vault) (secrets), [`symaira-ingest`](https://github.com/danieljustus/symaira-ingest) (OCR)
- **Delegates (does not rebuild):** spreadsheets → LibreOffice; code editing → editor plugins + agent orchestration; iCloud sync → the OS
- **Language:** Go (CGO-free, core) + Swift/SwiftUI (app). **Status:** planning. **License:** Apache-2.0
