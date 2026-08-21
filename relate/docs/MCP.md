# MCP server

`symrelate mcp` runs a narrow, versioned [Model Context
Protocol](https://modelcontextprotocol.io) server on stdio for agent
callers that need read/search access to contacts and a small, explicit set
of mutations. It shares the exact same service layer (`internal/app.App`)
as the CLI and the [local web console](CONSOLE.md) — there is exactly one
implementation of every business rule, not one per front end.

## Starting it

```sh
symrelate mcp
```

The server reads newline-delimited JSON-RPC 2.0 requests from stdin and
writes one JSON-RPC response per line to stdout. **Stdout carries protocol
frames only** — diagnostics (a "listening on stdio" line, read errors) go
to stderr, exactly like every other `symrelate` command's diagnostic
output. This makes stdout safe to pipe directly into an MCP client without
a startup banner corrupting the first frame.

Add it to an MCP client's config as a stdio server, e.g.:

```json
{
  "mcpServers": {
    "symrelate": { "command": "symrelate", "args": ["mcp"] }
  }
}
```

## Protocol

- Transport: newline-delimited JSON-RPC 2.0 over stdio (one JSON object
  per line, both directions).
- `protocolVersion`: `2024-11-05` (`internal/mcp.ProtocolVersion`). The
  server always responds with this fixed value on `initialize` regardless
  of what the client requests; bump this constant (and this line) when the
  implementation is updated to a newer MCP revision.
- Supported methods: `initialize`, `ping`, `tools/list`, `tools/call`.
  Any other method returns a JSON-RPC `-32601 method not found` error.
- Malformed JSON returns `-32700 parse error` rather than closing the
  connection — one bad line never kills the session.
- Requests without an `id` are JSON-RPC notifications and receive no
  response, per spec.

## Tool-call result shape

Every `tools/call` response wraps the tool's return value in both a text
block (for clients that only render `content`) and `structuredContent`
(the same value as real JSON, for clients that parse results directly):

```json
{
  "content": [{"type": "text", "text": "{\n  \"ID\": \"...\",\n  ...\n}"}],
  "structuredContent": {"ID": "...", "...": "..."},
  "isError": false
}
```

A tool that fails during execution (not-found, validation, conflict) sets
`isError: true` with a redacted message in `content` — this is an in-band
result, not a JSON-RPC error, so a client can tell "the tool ran and
reported a failure" apart from "the call itself was malformed" (unknown
tool name, unparsable arguments — those *are* JSON-RPC errors, since no
retry with different arguments changes the outcome).

## Tools

All field names are `snake_case`. Read tools never mutate; mutation tools
are named explicitly (`_create`, `_update`) so an agent cannot mistake a
search for a write.

| Tool | Kind | Description |
|---|---|---|
| `contact_search` | read | Search/list people by name substring and/or classification, paginated |
| `contact_get` | read | Load one person by id, with contact points/aliases/tags/classifications |
| `contact_create` | mutation | Create a new person — explicit, deliberate only |
| `contact_update` | mutation | Patch a person's display name and/or notes |
| `organization_search` | read | Search/list organizations by name substring and/or classification |
| `organization_get` | read | Load one organization by id |
| `followup_list` | read | List follow-ups for a person/organization, optionally filtered |
| `timeline_get` | read | Combined interaction + follow-up timeline for a person/organization |

Every tool's `description` field (visible to the calling agent via
`tools/list`) states its read/mutation nature and, for `contact_create`,
explicitly warns against auto-ingesting contacts discovered incidentally
(a meeting attendee, an email sender) — creation is only for a person the
user explicitly asked to add. This mirrors the same "reviewed, not
automatic" principle documented in [PRIVACY.md](PRIVACY.md) for imports
and in the SymMemory/SymMeet integration issues for external candidate
matching.

### Bounds and validation

- Pagination (`limit`/`offset` on the `_search` tools) is clamped
  server-side to `internal/domain/page.MaxLimit` (200) — the same bound
  the CLI's `--limit` flag is subject to. No tool can be made to return an
  unbounded result set.
- Every tool's arguments are decoded with unknown fields rejected, so a
  typo'd or unexpected argument name fails loudly (`isError: true`)
  instead of being silently ignored.
- Error messages are passed through the same redaction path
  (`internal/domain/security.Redact`) as CLI stderr — a contact-point
  value that ends up embedded in a database error can never reach an MCP
  client.

## What is intentionally not exposed

Delete/erase, tag/classify, backup/restore, export, and the import
workflow are CLI-only for the beta — the smallest safe agent surface is
read/search plus the two contact mutations above; broader automation is
explicitly out of scope for this beta (see the parent issue's "Out of
Scope" section). Extending the tool catalogue is an additive `v1` change
(new tool, unaffected existing tools) unless it removes or changes an
existing tool's shape, which requires an `api_version` bump — see
[CLI_CONTRACT.md](CLI_CONTRACT.md).
