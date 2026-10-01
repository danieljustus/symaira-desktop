# Rules & Settings contract

SymDesk exposes classification and mail-ingest configuration through `symdesk rules` and `symdesk mail rules`. The desktop client uses its resolved local or remote transport and validates `schema_version: 1`; no standalone `symingest` binary is required.

## Classification rules

The Cobra CLI accepts the shared flags after the subcommand. Put `--` before positional values so patterns or text starting with a dash are not parsed as flags:

```text
symdesk rules list --json [--vault <path>]
symdesk rules add --json [--vault <path>] -- <pattern> <kind> <value>
symdesk rules update --json [--vault <path>] -- <id> <pattern> <kind> <value>
symdesk rules test --json [--vault <path>] -- <text>
symdesk rules dry-run --json [--vault <path>] -- <pattern> <kind> <value>
symdesk rules delete --json [--vault <path>] -- <id>
```

Every successful response includes `schema_version: 1`:

- `list`: `{ "schema_version": 1, "rules": [...] }`
- `add` / `update`: `{ "schema_version": 1, "rule": { ... } }`
- `test`: `{ "schema_version": 1, "matches": [...] }`
- `delete`: `{ "schema_version": 1, "id": 123, "deleted": true }`
- `dry-run`: `{ "schema_version": 1, "operation": "dry_run", "matches": [...], "skipped": [...] }`

The dry-run scans existing indexed documents and returns safe metadata only: document ID, note path, title, matched existing rule IDs, and skip reasons. It does not return note bodies or write documents.

## Mail-ingest rules

Mail configuration is available through a separate versioned JSON contract:

```text
symdesk mail rules list --json [--config <config-path>]
symdesk mail rules create --json [--config <config-path>] < account.json
symdesk mail rules update <account-id> --json [--config <config-path>] < account.json
symdesk mail rules delete <account-id> --json [--config <config-path>]
```

Supported CLI operations are `list`, `create`, `update`, and `delete`. Create and update read one account's JSON from stdin. Without `--config`, the CLI resolves the local mail configuration or the global configuration; mail accounts are not vault-scoped. Writes preserve unrelated TOML content and are atomic. Write responses include `reload_required: true`; an already-running watcher is not hot-reloaded.

Password safety rules:

- secret references such as `symvault://...` remain visible as references
- bare values are treated as environment-variable names and fail if unset
- plaintext password values are returned only as `<redacted>`
- updates that omit `password_secret` preserve the existing value
- the desktop UI never displays resolved credentials
