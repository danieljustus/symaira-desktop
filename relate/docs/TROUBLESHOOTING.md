# Troubleshooting

Start with `symrelate doctor` — it never prints contact data (only paths
and connectivity, see [PRIVACY.md](PRIVACY.md)), so its output is always
safe to paste into a bug report.

```sh
symrelate doctor
```

## "database: FAIL" / "ping: FAIL"

`doctor` prints the resolved config/data/cache directories and the
database path before the failure. Check:

- The data directory (`symrelate doctor`'s `data dir:` line, or
  `$XDG_DATA_HOME/symrelate` / `~/.local/share/symrelate` by default) is
  writable.
- No other process holds an exclusive lock on `symrelate.db` — SQLite
  uses WAL mode with a single writer connection
  (see [ARCHITECTURE.md](../ARCHITECTURE.md)), so concurrent `symrelate`
  invocations from the same profile should not normally conflict, but a
  crashed process holding a stale lock can. Restarting resolves this in
  virtually all cases.

## "failed to bind the console listener: ... address already in use"

Another process (possibly another `symrelate console`) is already using
that port. Either stop it or pick a different port:

```sh
symrelate console --port 0   # let the OS choose a free port
```

## `symrelate console` — browser shows "unauthorized"

The session token from the URL `symrelate console` printed to stderr is
required once per session (see [CONSOLE.md](CONSOLE.md)). If you closed
that terminal, restart the console and use the newly printed URL — a
stale token from a previous run's process is never valid for a new one.

## `symrelate mcp` — client can't parse a response / sees garbage

Confirm nothing else writes to the process's **stdout** — MCP requires
stdout to carry JSON-RPC frames exclusively (see [MCP.md](MCP.md)). If
you are wrapping `symrelate mcp` in a shell script or another tool, make
sure that wrapper does not add its own banner or logging to stdout;
route any wrapper diagnostics to stderr instead.

## `memory status` / `meeting status` report unavailable

Both SymMemory and SymMeet are optional — `symrelate` is fully functional
without either installed (see [ARCHITECTURE.md](../ARCHITECTURE.md)'s
standalone-first rule). "unavailable" means the respective binary was not
found on `PATH`, didn't respond within the bounded timeout, or returned
something `symrelate` could not parse as a compatible version. Installing
or upgrading the sibling tool and re-running `status` is the fix; no
`symrelate` command depends on either being present.

## Import: "duplicate contact point" / row skipped as a validation issue

This is by design — `import vcard`/`import csv --dry-run` always shows
what would happen before anything is written (see the main
[README.md](../README.md#importing-existing-contacts)). Re-run with
`--resolve ROW=create|skip|merge:PERSONID` for each row the dry run
flagged as a duplicate candidate.

## A contact-point value appears masked as `[redacted-email]` / `[redacted-phone]`

That is [PRIVACY.md](PRIVACY.md)'s redaction working as intended — every
error path masks email- and phone-shaped substrings before they reach
stderr, an MCP response, or the console's API. If you need the actual
value for debugging, look it up directly with `contact show <id>` (whose
JSON output is not redacted — the redaction applies to error/diagnostic
text specifically, not to the deliberate data commands).

## Filing a bug report

Include:

- `symrelate version --json` output.
- `symrelate doctor`'s output (safe to share — no contact data).
- The exact command and flags (redact any contact-point values you typed
  as arguments yourself — the tool does not redact your own command
  line).
- Whether the issue reproduces against a fresh profile (`SYMRELATE_DATA_HOME=$(mktemp -d) symrelate doctor`).
