# Local web console

`symrelate console` runs a localhost-only, authenticated web UI for
everyday contact management — for regular use without CLI or MCP
fluency. It is a thin HTTP layer over the exact same `internal/app.App`
service boundary the CLI and [MCP server](MCP.md) use: there is exactly
one implementation of every business rule, not one per front end.

## Starting it

```sh
symrelate console               # picks a free port
symrelate console --port 4287   # bind a specific port
```

```
symrelate console: listening on http://127.0.0.1:4287/?token=<64 hex chars>
symrelate console: open the URL above in a browser; the token is required once to start a session
```

Open the printed URL in a browser. That first request establishes an
authenticated session (see below); every subsequent page load and API
call reuses it.

## Localhost-only, authenticated by design

- The listener is always bound to **127.0.0.1**, never `0.0.0.0` or a
  wildcard — `internal/console.Listen` hard-codes the loopback address, so
  the console (and, through it, every contact in the database) is never
  reachable from the local network, let alone the internet.
- A fresh random 256-bit session token is generated on every `symrelate
  console` start (`internal/console.GenerateToken`) and printed to
  **stderr** only — never logged to a file, never sent anywhere.
- The first request must carry `?token=<token>` in the URL. The server
  verifies it (constant-time comparison), sets an `HttpOnly`,
  `SameSite=Strict` session cookie, and redirects to the same URL with the
  token stripped — so the token never lingers in browser history past that
  first load. Every request after that — page loads, `/api/v1/*` calls —
  requires either that cookie or an `Authorization: Bearer <token>` header
  (for programmatic callers). A request without a valid session gets `401
  Unauthorized`; there is no page or API response served without one.
- The token is also written to `<data-dir>/console-token` (mode `0600`)
  as a convenience for a companion process on the same machine — this
  file is advisory only, the running server always trusts the in-memory
  token it started with, so a stale or tampered file can never grant
  access.

## What it does

A single-page vanilla HTML/CSS/JS UI (`internal/console/static/`, no
build step, no external CDN, no telemetry — see
[ARCHITECTURE.md](../ARCHITECTURE.md)'s standalone-first rule) against a
JSON API under `/api/v1/*`:

| Area | Capability |
|---|---|
| Contacts | search/list, create, view (contact points, memberships, timeline), edit, **erase** |
| Organizations | search/list, create, view, edit, erase |
| Memberships | link a person to an organization with a role |
| Follow-ups | list/filter, create, complete, cancel (from a contact's detail view) |
| Import | dry-run preview of a vCard/CSV file already on this machine, review duplicate candidates, apply with a reviewed create/skip/merge choice per row — the same workflow `import vcard`/`import csv` implements on the CLI |

"Erase" (not a bare delete) calls the same audited privacy-erasure path
the CLI's `contact erase` / `org erase` use
(`internal/service/security.Service.EraseContact/EraseOrganization`, see
[PRIVACY.md](PRIVACY.md)) — the console never takes a UI-owned shortcut
around that audit trail.

Import in the console reads a file path already present on the machine
the console is running on — this is a single-user, localhost-only tool
with no upload step, exactly like the CLI's `import vcard <file>` reading
a local path.

## Accessibility

Every control is a native `<button>`, `<input>`, `<select>` or `<label
for=…>` — keyboard navigation (Tab/Shift+Tab, Enter/Space to activate) is
the browser's own, not reimplemented. An `aria-live="polite"` status
region announces the result of an action (created, error) for assistive
technology. Empty, loading and error states are always rendered as text,
never left blank.

## Not in the console

Backup/restore, export, audit-log viewing and the reviewed
SymMemory/SymMeet integration commands (see the parent issues) are
CLI-only for the beta, matching MCP's "smallest safe surface first"
scoping — see [MCP.md](MCP.md#what-is-intentionally-not-exposed).
