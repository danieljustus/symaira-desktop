## What's changed

### Features
- #261 Paperless-ngx importer — migrate archives into the vault
- #262 IMAP mail ingestion — end-to-end from account setup to indexed notes
- #263 Storage-path templating, retention rules, and PDF/A archive generation
- #264 Barcode-based multi-document scan splitting during ingest
- #265 Users, groups and document-level permissions for the self-hosted server
- #267 Expiring share links for unauthenticated document access

### Fixes
- #243 Doctor report preserved when CLI exits non-zero (#260)
- #244 Companion Tools correctly reports tool status (#259)
- #245 Dashboard shows recent notes on first display (#259)
- #246 CLI version check prevents silent use of older symdesk (#260)
- #247 Validation errors reported at the fields they concern (#259)
- #248 Command palette closes with Escape key (#259)
- #249 Menu bar commands and Cmd+, Settings shortcut (#260)
- #273 Share routes wired into server mux (fix for #267 handlers)

### Dependencies
- #268 codeql-action/autobuild 4.37.1 → 4.37.3
- #269 docker/login-action 4.4.0 → 4.5.2
- #270 symaira-corekit 0.5.0 → 0.6.0
- #271 codeql-action/analyze 4.37.1 → 4.37.3
- #272 codeql-action/init 4.37.1 → 4.37.3

### Docs
- #266 Browser-access implementation proposal
- Updated AGENTS.md, README.md, SELF_HOSTING.md and VAULT.md for new features

**Full Changelog**: https://github.com/danieljustus/symaira-desktop/compare/v0.7.1...v0.7.2
