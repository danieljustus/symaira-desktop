## What's changed

### Features
- #402 iOS vault-grounded AI chat — streaming answers with citations
- #383 PostgreSQL storage backend and abstracted server state
- #369 Multiple named vaults with switching and creation
- #387 Agentic tool loop for the in-app AI — registry-backed, read-only, bounded
- #411 Externalize large tool results outside the conversation
- #412 Task-scoped checkpoints for agent runs — undo as a unit
- #422 Retention for externalized agent results; surface results size in doctor
- #421 Protect checkpoint-referenced blobs from prune GC; checkpoint retention
- #410 Vault health scan and repair plan
- #409 Warn on citations to unread files
- #385 On-device AI fallback — automatic provider selection
- #386 Inline AI actions on notes and selections — preview, accept, undo
- #371 Streaming AI ask/transform endpoints with citations and permission scoping
- #381 App-owned model downloads with pinned revision, checksum and cancel/resume
- #382 Golden-vector verification for on-device embeddings
- #367 iOS write layer — offline outbox and conflict resolution
- #368 iOS ranked search index — fast, persisted, prefix + snippets
- #389 App Intents, quick actions and a recents widget for iOS
- #372 iOS recents + Spotlight + quick-open
- #373 iOS filters and search operators — chips + desktop grammar
- #358 Persisted recently-opened list in the iOS app
- #374 History & trash app surface — version diff, per-note history/trash access
- #379 Duplicates lane, real export and CLI-only rationale
- #370 Complete tag browser — rename/merge/delete, hierarchy, autocomplete, clickable chips
- #376 Guided Paperless-ngx import in settings and onboarding
- #388 Shared design system — type scale, text fields, settings chrome
- #334 Tag browser UI with counts and click-to-filter
- #335 Folder tree in sidebar replaces flat note list
- #333 Search filter UI with tag and type chips
- #336 Version history and trash UI surfaces
- #318 Classify files as notes or documents with persisted type
- #319 Config schema validation with actionable startup errors
- #353 AI settings pane; Ollama URL folded into config
- #355 File menu command and shortcut for the daily note
- #341 Finder Favorites sidebar registration
- #343 Content preview thumbnails on document and note cards
- #344 Consume folder settings and monitoring UI
- #345 Editable document inspector fields with save
- #346 Back navigation with history stack
- #363 Route worker OCR through symingest
- #361 Named vault registry foundation

### Fixes
- #421 Checkpoint-referenced blobs protected from prune GC
- #416 Externalized AI summaries kept valid UTF-8 at rune boundaries
- #409 Citation warnings for unread files
- #366 Visible error when image paste cannot store a vault asset
- #354 Finder Favorites registration opt-in and crash-safe
- #342 Wire vault root for image paste/drop in editor
- #340 UI layout fixes — New Note button, clipped header, AI Dock overflow
- #337 User-visible error banners replace console-only errors
- #314 Sidecar index pruning for deleted and ignored files
- #313 Debounced markdown highlighting for reliable typing
- #312 No error text written into note content on load failure
- #359 Mark two pure @MainActor-isolated view helpers nonisolated

### Dependencies
- #391 symaira-corekit 0.6.0 → 0.8.0
- #390 modernc.org/sqlite 1.54.0 → 1.55.0
- #392-394 codeql-action 4.37.3 → 4.37.4
- #395 docker/login-action 4.5.2 → 4.6.0

### Docs & housekeeping
- #401 Code of Conduct; fix flaky streaming timing test
- #384 On-device OCR port evaluated against German corpus, adoption rejected

**Full Changelog**: https://github.com/danieljustus/symaira-desktop/compare/v0.7.3...v0.8.0
