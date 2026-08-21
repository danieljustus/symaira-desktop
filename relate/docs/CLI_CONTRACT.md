# CLI JSON contract

`symrelate`'s scripted surface — the CLI's JSON output and the MCP tool
catalogue (see [MCP.md](MCP.md)) — is versioned together as **API
version `v1`** (`internal/version.APIVersion`, surfaced as `api_version`
in `symrelate version --json`). This document is the compatibility
contract referenced by the beta acceptance criteria.

## Versioning independent of schema and release version

`symrelate version --json` reports three independent numbers:

```json
{"tool":"symrelate","version":"v0.2.1-beta.1","schema_version":7,"api_version":"v1"}
```

- `version` — the release tag, changes on every release.
- `schema_version` — the highest applied SQLite migration; changes when
  the on-disk schema grows (see `internal/storage/sqlite/migrate.go`).
- `api_version` — the JSON/MCP wire contract described here; stays fixed
  across releases and schema migrations unless a breaking change is
  introduced.

## Compatibility rules

Within `v1`:

- Fields are **additive only**. A new field may appear in any JSON
  response or MCP tool result at any time; existing fields are never
  renamed, retyped, or repurposed.
- A field is **removed only** by bumping `api_version` to `v2` in a release
  that documents the break in `CHANGELOG.md`.
- Every JSON field name is `snake_case` in MCP tool schemas and in the
  domain types marshaled by the CLI's `--json` (default) output path
  (Go's default struct-field JSON tags — verified by
  `internal/version/version_test.go`'s `TestInfo_JSONUsesSnakeCase` for
  the version payload, and by the MCP tool schemas directly).
- Error responses never include raw contact-point values — see
  [PRIVACY.md](PRIVACY.md)'s redaction policy. This applies identically
  to CLI stderr, MCP JSON-RPC errors and MCP in-band tool-result errors.
- Pagination is bounded server-side (`internal/domain/page`,
  `MaxLimit = 200`) — no CLI flag or MCP tool argument can request an
  unbounded result set.

## Machine and human output

Every command that returns structured data supports two output modes:

- **JSON (default)** — stable, versioned, script-safe. This is the
  contract described above.
- **`--human`** — a short, readable summary of the same data for
  interactive use (e.g. `symrelate contact show --human <id>`). Core
  contact, relationship, interaction and follow-up types get dedicated
  prose/table rendering (`internal/cli/human.go`); composite report
  payloads without a dedicated renderer yet (import plans/results, audit
  events) fall back to indented JSON under `--human` — still readable,
  just not prose. `symrelate version` is the one command with the
  opposite default (short text by default, `--json` opt-in) because it is
  a diagnostic command primarily read by a human running `doctor`-style
  checks.

`--human` output has no compatibility guarantee — only JSON does. Do not
parse `--human` output in scripts.

## Published shapes

Shapes published for external consumers have their own contract documents:

- [integrations/CONTACT_REF.md](integrations/CONTACT_REF.md) — the
  reference-only contact shape (`symrelate contact ref <id>`) external
  tools may store and resolve without receiving contact-point data. Its
  `schema_version` follows the additive-only rule above.

## Transport hygiene

Diagnostics and progress output always go to **stderr**. **Stdout**
carries only the command's result (`--json` payload or `--human` text) or,
for `symrelate mcp`, JSON-RPC protocol frames exclusively — see
[MCP.md](MCP.md).
