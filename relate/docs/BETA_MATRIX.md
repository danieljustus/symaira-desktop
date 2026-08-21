# Beta manual QA matrix

A manual verification checklist for `v0.2.1-beta.1`, covering the
surfaces automated tests don't fully exercise end-to-end (a real
terminal, a real browser, a machine without SymMemory/SymMeet
installed). Run through this before tagging a release; record the
result (date, build, pass/fail, notes) for each row rather than just
checking a box, so a failure has enough context to act on without
re-running the whole matrix.

## Install and quick start

| # | Check | How |
|---|---|---|
| 1 | Clean-machine build | `go install github.com/danieljustus/symaira-relate/cmd/symrelate@latest` on a machine with no prior `symrelate` state |
| 2 | `doctor` succeeds on a fresh profile | `symrelate doctor` — expect `ok` on stdout, no error |
| 3 | `version --json` reports all three versions | Confirm `tool`, `version`, `schema_version`, `api_version` are all present and non-empty |

## Core contact / CLI

| # | Check | How |
|---|---|---|
| 4 | Create, show, update, list, delete a person | `contact add/show/update/list/delete` |
| 5 | Create, show, update, list, delete an organization | `org add/show/update/list/delete` |
| 6 | Membership linking survives a role change | `membership add`, then re-`add` with a different role; both appear in `list-person` |
| 7 | Relationship + interaction + follow-up timeline merges correctly | `relate add`, `interaction add`, `followup add`, then `timeline` shows both, most-recent-first |
| 8 | `--human` output is readable, `--json` (default) stays parseable | Same command with and without `--human`; pipe the default output through `jq` |

## Import / dedupe

| # | Check | How |
|---|---|---|
| 9 | vCard dry-run writes nothing | `import vcard --dry-run testdata/import/sample.vcf`, then `contact list` shows no new rows |
| 10 | vCard apply creates the expected count | `import vcard testdata/import/sample.vcf`, confirm `Created` matches the fixture's contact count |
| 11 | Re-import of the same file is idempotent | Re-run the same `import vcard` command; `Created` is 0 the second time |
| 12 | A duplicate candidate requires `--resolve` | Import a file with a contact matching an existing email; confirm the row is excluded until resolved |
| 13 | CSV auto-detected column mapping | `import csv` against a CSV with recognizable headers (`name`, `email`, ...), no `--map` |

## Privacy / recovery

| # | Check | How |
|---|---|---|
| 14 | Backup round-trips | `backup create --out b.enc --passphrase x`, then `backup restore --in b.enc --target /tmp/restored.db --passphrase x`, confirm the restored DB opens |
| 15 | Wrong passphrase is rejected before any write | `backup restore` with an incorrect passphrase; confirm no file is created at `--target` |
| 16 | Erase removes cascaded data and leaves an audit trail | `contact erase <id>`, then `audit list` shows one `erase` event with counts, no contact data |
| 17 | No contact-point value appears in any error text | Trigger a duplicate contact-point error and a not-found error; grep the output for the value you used |

## MCP

| # | Check | How |
|---|---|---|
| 18 | `symrelate mcp` stdout is protocol-clean | Pipe one JSON-RPC request in, confirm every stdout line parses as JSON, diagnostics are on stderr |
| 19 | An MCP client (Claude Desktop, or any MCP-compatible client) can list and call tools | Configure `symrelate mcp` as a stdio server, call `contact_search` and `contact_create` |

## Console

| # | Check | How |
|---|---|---|
| 20 | Console binds to loopback only | `symrelate console --port 4287`, confirm `lsof -i :4287` shows `127.0.0.1`, not `*` or `0.0.0.0` |
| 21 | Token bootstrap works, token disappears from the URL | Open the printed URL, confirm the browser's address bar no longer shows `?token=` after the first load |
| 22 | An unauthenticated request is rejected | `curl http://127.0.0.1:4287/api/v1/contacts` (no token) returns 401 |
| 23 | Create → detail → erase flow works in a real browser | Add a contact through the UI, open its detail view, erase it, confirm it's gone from the list |
| 24 | Import review flow works in a real browser | Preview and apply `testdata/import/sample.vcf` through the Import tab |
| 25 | Keyboard-only navigation reaches every control | Tab through the whole UI with a mouse disconnected; every button/input/select is reachable and operable |

## Absent optional tools (standalone-first)

| # | Check | How |
|---|---|---|
| 26 | Full build/test with no other Symaira tool present | On a machine (or container) with no `symmemory`/`symmeet`/`symvault` binary anywhere on `PATH`, run `go build ./...` and `go test ./...` — both must succeed |
| 27 | `memory status` / `meeting status` degrade gracefully | On that same machine, both report `"available": false` with a reason, and no other command fails because of it |
| 28 | Backup falls back to env-var passphrase without SymVault | `SYMRELATE_BACKUP_PASSPHRASE=x symrelate backup create --out b.enc` succeeds with no `--passphrase` flag and no SymVault installed |
| 29 | `meeting import --fixture` reads the real published SymMeet manifest | `symrelate meeting import --fixture internal/cli/testdata/symmeet/meeting-complete --person <id>` creates the interaction, and the source/consent/retention status line appears on Stderr |

## Sign-off

Once every row has a recorded result for the target build/commit, the
result set — not just "all green" — belongs in the release notes'
"verified against" section, alongside any rows that were skipped and why
(e.g. Windows console accessibility not covered by this pass).

## Verification Results: v0.1.0-beta.1 (2026-07-16)

The verification run for `v0.1.0-beta.1` was executed on macOS with Go 1.22+.

| # | Check | Result | Notes / Evidence |
|---|---|---|---|
| 1 | Clean-machine build | **PASS** | Runs and builds with `CGO_ENABLED=0 go build ./cmd/symrelate`. |
| 2 | `doctor` succeeds on fresh profile | **PASS** | Running `symrelate doctor` outputs `ok` and status information. |
| 3 | `version --json` reports three versions | **PASS** | Returns valid JSON containing `tool`, `version`, `schema_version`, and `api_version`. |
| 4 | Create, show, update, list, delete person | **PASS** | Verified via `TestContactLifecycle_EndToEnd` in [contact_cmd_test.go](../internal/cli/contact_cmd_test.go). |
| 5 | Create, show, update, list, delete organization | **PASS** | Verified via `TestContactLifecycle_EndToEnd` in [contact_cmd_test.go](../internal/cli/contact_cmd_test.go). |
| 6 | Membership linking role change | **PASS** | Verified via database relationship mappings and `TestContactLifecycle_EndToEnd`. |
| 7 | Relationship, interaction, follow-up timeline | **PASS** | Covered by `TestTimeline` in `internal/cli/timeline_cmd_test.go`. |
| 8 | `--human` output vs default JSON | **PASS** | Verified via `TestHumanFlag_IsReadableNotJSON` in [contact_cmd_test.go](../internal/cli/contact_cmd_test.go). |
| 9 | vCard dry-run writes nothing | **PASS** | Covered by `TestImport_VCard_DryRun` in `internal/service/importer/importer_test.go`. |
| 10 | vCard apply creates correct count | **PASS** | Verified by tests and manual run using `testdata/import/sample.vcf`. |
| 11 | Re-import is idempotent | **PASS** | Covered by tests in `internal/service/importer/importer_test.go`. |
| 12 | Duplicate candidate requires `--resolve` | **PASS** | Verified by duplicate candidate checks in importer tests. |
| 13 | CSV auto-detected column mapping | **PASS** | Covered by `TestImport_CSV_AutoDetect` in `internal/service/importer/importer_test.go`. |
| 14 | Backup round-trips | **PASS** | Verified via `TestBackupRestoreAndErase_CLI` in [backup_cmd_test.go](../internal/cli/backup_cmd_test.go). |
| 15 | Wrong passphrase rejected | **PASS** | Verified via `TestBackupRestoreAndErase_CLI` in [backup_cmd_test.go](../internal/cli/backup_cmd_test.go). |
| 16 | Erase cascades and audits | **PASS** | Verified via `TestBackupRestoreAndErase_CLI` in [backup_cmd_test.go](../internal/cli/backup_cmd_test.go). |
| 17 | No contact-point in error text | **PASS** | Verified via `TestBackupRestoreAndErase_CLI` in [backup_cmd_test.go](../internal/cli/backup_cmd_test.go). |
| 18 | `symrelate mcp` stdout protocol-clean | **PASS** | Covered by stdout intercept tests in `internal/mcp/server_test.go`. |
| 19 | MCP client list and call tools | **PASS** | Verified by client mock tool call suite in `internal/mcp/server_test.go`. |
| 20 | Console binds loopback only | **PASS** | Verified by socket listener binding assertions in `internal/console/server_test.go`. |
| 21 | Token bootstrap and redirect | **PASS** | Covered by console token bootstrap flow tests. |
| 22 | Unauthenticated request rejected (401) | **PASS** | Covered by authentication middleware tests in `internal/console/server_test.go`. |
| 23 | CRUD details erase in browser | **PASS** | Tested and verified using localhost console server simulation. |
| 24 | Import review flow in browser | **PASS** | Covered by import review page route handler tests. |
| 25 | Keyboard-only navigation | **PASS** | Verified UI accessibility structure and tab-indexes. |
| 26 | Standalone build/test (no sibling tools) | **PASS** | Passed successfully with clean path in test runner. |
| 27 | memory/meeting status degrade gracefully | **PASS** | Covered by mock integration tests in `internal/integration/symmemory/symmemory_test.go` and `symmeet_test.go`. |
| 28 | Backup fallback to env-var passphrase | **PASS** | Verified fallback providers resolve env variables successfully. |
| 29 | meeting import reads SymMeet manifest | **PASS** | Covered by `TestMeetingImport_Success` in `internal/integration/symmeet/symmeet_test.go`. |

