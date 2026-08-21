# Changelog

All notable changes to `symrelate` are documented here. The format is
loosely based on [Keep a Changelog](https://keepachangelog.com/); dates
are omitted until the first tagged release.

## [Unreleased]

### Changed

- Update `modernc.org/sqlite` to v1.56.0 (#77).
- Update `github.com/danieljustus/symaira-corekit` to v0.9.1 (#78).
- Update the Go toolchain to 1.26.6 (#79).

## [v0.2.1-beta.1] - 2026-08-05

### Fixed

- Make quick-add atomic: creating a person and adding optional email/phone
  contact points now commits or rolls back as one transaction, preventing
  half-created people after a contact-point failure (closes #68).

### Changed

- Update `modernc.org/sqlite` to v1.55.0.

## [v0.2.0-beta.1] - 2026-07-31

### Added

- Replace the hand-written MCP JSON-RPC transport with the shared
  `corekit/mcpserver` transport and add the ecosystem versionkit handshake.

## [v0.1.2-beta.1] - 2026-07-30

### Added

- Update detection via `symrelate version --check`: checks GitHub for newer
  releases using `updatecheck` (corekit), never blocks, and swallows errors
  silently (closes #60).

## [v0.1.1-beta.1] - 2026-07-21

### Added

- Publish a reference-only contact-reference JSON contract for external consumers, with CLI lookup `symrelate contact ref <id>` (closes #57).

## [v0.1.0-beta.1] - 2026-07-19


### Added

- Core contact, organization, and contact-point model with aliases, tags,
  and classifications.
- Directed relationships, an interaction timeline, and follow-up
  reminders.
- Data protection: AES-256-GCM encrypted backup/restore, audited erase,
  JSON/CSV export, and contact-point redaction across every error path.
- Reviewed vCard and CSV import with duplicate detection and an explicit
  create/skip/merge resolution per row; idempotent on re-import.
- A narrow MCP server (`symrelate mcp`) over stdio JSON-RPC 2.0, sharing
  the same service layer as the CLI: `contact_search`/`get`/`create`/
  `update`, `organization_search`/`get`, `followup_list`, `timeline_get`.
- A documented, versioned CLI JSON contract (`api_version` in `version
  --json`; see `docs/CLI_CONTRACT.md`) and an opt-in `--human` flag for
  readable output on every data command.
- A local web console (`symrelate console`): a localhost-only,
  token-authenticated UI for contact/organization CRUD (including
  audited erase), memberships, follow-ups, and reviewed import review —
  see `docs/CONSOLE.md`.
- An optional SymMemory context adapter: runtime discovery, reviewed
  contact-to-entity links, and a best-effort context lookup — see
  `docs/integrations/SYMMEMORY.md` for what's implemented versus blocked
  on upstream work.
- An optional SymMeet integration: runtime discovery, idempotent
  meeting-interaction import from the real published SymMeet fixture
  format, and source/consent/retention status display — see
  `docs/integrations/SYMMEET.md` for what's implemented versus blocked on
  upstream work.
- CI: format/vet/build/test on every push and PR, plus a `govulncheck`
  job.
- A tag-triggered release workflow building cross-platform binaries
  (linux/darwin/windows, amd64/arm64) with checksums.

### Known limitations for the beta

See the "Known limitations" section of `README.md` and
`docs/BETA_MATRIX.md` for the full list — in short: SymMemory candidate
matching/relation writes are blocked on still-open issues upstream, and
beta CLI/API surfaces (schema, JSON contract, on-disk format) may still
change between beta releases.
