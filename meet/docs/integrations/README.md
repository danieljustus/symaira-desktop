# Integration Contract

This directory specifies how SymDesk, Memory, Seek, Print, and appkit
integrate with `symmeet`. Every cross-repository integration uses only
the files documented here — never internal Symaira packages.

## Contract version

The current contract version is **1** (`schema_version: 1`). Additive
changes (new optional fields, new tools) are non-breaking. Renaming or
removing fields requires a new major version.

## Commands

```bash
symmeet capabilities --json
symmeet meeting list --json
symmeet meeting show <meeting_id> --json
symmeet export <meeting_id> --format markdown --output -
symmeet mcp
```

## Artifacts

All meeting artifacts live under the user's data directory as portable
files. The manifest (`manifest.json`) is the source of truth for meeting
metadata. Segments live in `segments.raw.jsonl` and optionally
`segments.edited.jsonl`.

## Timeouts

Subprocess calls from sibling tools should use these timeouts:

| Operation | Timeout |
|-----------|---------|
| `meeting list` | 5s |
| `meeting show` | 5s |
| `export` | 30s |
| `capabilities` | 2s |
| MCP `initialize` | 5s |
| MCP `tools/call` | 60s |

Exit code `0` means success. Exit code `1` means runtime failure.
Exit code `2` means invalid input. Exit code `3` means permission denied.
Exit code `4` means unsupported operation.

## Error handling

Every `--json` command writes exactly one JSON document to stdout on
success, or writes an error to stderr and exits with the appropriate
code. Sibling tools must not parse stderr as JSON.

## Privacy

- No participant names or transcript content in capability output.
- No absolute user paths in machine-readable output.
- Recording requests always require interactive human authorization.
