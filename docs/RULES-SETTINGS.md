# Rules & Settings contract

SymDesk delegates classification and mail-ingest configuration to the installed `symingest` binary. The desktop client validates `schema_version: 1` and reports missing or incompatible binaries without breaking the rest of the app.

## Classification rules

Flags must precede the subcommand because `symingest` uses Go's standard flag parser:

```text
symingest rules --json [--vault <path>] list
symingest rules --json [--vault <path>] add <pattern> <kind> <value>
symingest rules --json [--vault <path>] update <id> <pattern> <kind> <value>
symingest rules --json [--vault <path>] test <text>
symingest rules --json [--vault <path>] dry-run <pattern> <kind> <value>
symingest rules --json [--vault <path>] delete <id>
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
symingest mail --json --config ~/.config/symingest/config.toml list
symingest mail --json --config ~/.config/symingest/config.toml validate
symingest mail --json --config ~/.config/symingest/config.toml --input account.json create
symingest mail --json --config ~/.config/symingest/config.toml --input account.json --id <account-id> update
symingest mail --json --config ~/.config/symingest/config.toml --id <account-id> delete
```

Supported operations are `list`, `validate`, `create`, `update`, `delete`, and `replace`. Writes preserve unrelated TOML content and are atomic. Responses include `reload_required: true` and explicit next-watch-restart semantics; an already-running watcher is not hot-reloaded.

Password safety rules:

- secret references such as `symvault://...` remain visible as references
- plaintext password values are returned only as `<redacted>`
- updates that omit `password_secret` preserve the existing value
- the desktop UI never displays resolved credentials
