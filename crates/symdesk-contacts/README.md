# symdesk-contacts

Incremental Rust port of `internal/contacts/internal`. Go is still the
production implementation; this crate is not wired into `symdesk` yet.

## Implemented and oracle-tested

- All seven SQLite migrations, copied byte-for-byte into this crate. The Go
  oracle captures every `sqlite_master` object (including automatic indexes)
  and migration bookkeeping. Repeated migration preserves `applied_at`.
- UTF-8 CSV mapping/parsing, identity hashes, nil versus empty collections,
  malformed-record diagnostics, BOM behavior, quoting, CRLF, Unicode and header
  alias precedence: 77 Go-generated CSV cases.
- UTF-8 vCard parsing: 40 Go-generated cases covering folding, preferences,
  escaped values, groups, UID/hash identity and malformed-entry recovery.
  Decoder adaptation attribution is in `THIRD-PARTY-NOTICES`.
- Person/organization create, get, list, update and delete, including literal
  LIKE escaping, pagination, provenance, nil children and deletion cascades.
  Shared alias/tag/classification/contact-point APIs cover validation,
  idempotent inserts, preferred ordering, cross-parent removal, FK failures,
  duplicate conflicts and failed-tag transaction rollback: 116 Go operations
  with seven ordered database snapshots (`crud.json`).
- Directed person/person and person/organization relationships, idempotent
  retries, self-link/date/FK errors; follow-up creation/filtering and terminal
  state transitions; immutable interactions, ordering and most-recent lookup:
  95 Go operations with three ordered DB snapshots (`relationship.json`).
- Person/organization privacy erasure and audit listing: six Go cases check
  exact summaries/errors and all fourteen affected tables, including failed
  audit rollback, missing parents, import/memory-link cascades, shared-tag
  preservation and count-only audit details (`erase.json`). Audit clock and
  OS username are normalized; Rust receives an injected actor.
- Injected key-provider policy: 40 Go cases cover static/environment/default
  precedence, chain fallback versus fatal errors, empty-success semantics,
  non-TTY refusal and SymVault command arguments/stdout/error handling. The Go
  oracle copies its own test executable as a fake `symvault` into an isolated
  PATH; neither implementation reads real credentials. Rust's interactive
  terminal adapter policy is implemented but real-TTY parity is still open.
- Organization memberships (start/end, per-person and per-organization lists),
  quick-add, opaque-reference lookups (`get_ref`, `find_refs`): 60 Go
  operations with four DB snapshots (`membership-ref.json`).
- Combined timeline, meeting import (idempotent per `MeetingID`), interactions
  and follow-ups: 52 Go operations (`timeline-meeting.json`). SymMemory entity
  links, stored as opaque id/type pairs only: 30 operations (`memorylink.json`).
- `redact`: the Redact last-line-of-defense masks on RE2 semantics (ASCII
  `\d`, `\s` without `\v`), 35 inputs (`redact.json`).
- Data-minimized JSON/CSV export (Go `encoding/json` HTML escaping and indent,
  `encoding/csv` quoting, NOCASE ordering, audit rows, NULL-name failure) and
  AES-256-GCM/Argon2id encrypted backup and restore (`export-backup.json`): a
  Go-written backup restores here, 12 damaged variants fail with Go's exact
  error and leave the target untouched, and a Rust-written backup is restored
  by the Go service (`make contacts-security-differential`).
- Contact-point normalization: 36 Go-generated cases, including simple Unicode
  case mapping and non-ASCII decimal digits.
- Duplicate planning (exact source, normalized email/phone, SQLite NOCASE names)
  and explicit create/skip/merge application. Merge is additive; unresolved
  duplicates are skipped. Organization creation/membership, contact-point
  uniqueness, source linking and import-run counts are checked in ordered DB
  snapshots. Re-import outcomes are checked separately.

The Rust services receive ID and UTC RFC3339Nano timestamp suppliers.
CRUD uses google/uuid's injected entropy seam in Go; the production clock has
no seam, so only `CreatedAt`/`UpdatedAt` and their DB columns are normalized to
one documented instant after each operation. IDs and all other values are
compared exactly; clock acquisition/defaults are not certified. Relationship
inputs accept canonical UTC RFC3339Nano strings at the typed-time adapter
boundary. Follow-up transition clock fields are normalized too. Overdue/upcoming
oracle cases use years 2000 and 9000, not a wall-clock tolerance window; precise
clock-boundary and non-UTC time adapters remain uncertified.

The Rust apply function receives ID and UTC RFC3339Nano timestamp suppliers;
callers must supply UUIDs and correctly formatted clock values. Oracle snapshots
omit random primary keys and wall-clock timestamps, but preserve referenced
person name/source/given-name to distinguish same-named persons. Migration
bookkeeping timestamps are pinned explicitly and compared exactly.

## Reproduce

```sh
GOTOOLCHAIN=go1.26.10 go test ./internal/contacts/internal/storage/sqlite -run '^TestPortContactsSchemaContract$' -count=1
GOTOOLCHAIN=go1.26.10 go test ./internal/contacts/internal/domain/importer ./internal/contacts/internal/service/importer -run '^TestPortContacts(CSV|VCard|Import)Contract$' -count=1
cargo test -p symdesk-contacts --locked
```

Set `PORT_GENERATE=1` only for deliberate fixture generation; ordinary tests
recompute the real Go result and compare without writing. Make targets are
`contacts-schema-differential`, `contacts-import-differential`,
`contacts-vcard-differential`, `contacts-crud-differential`,
`contacts-relationship-differential`, `contacts-erase-differential`,
`contacts-keyprovider-differential`, `contacts-membership-differential`,
`contacts-timeline-differential`, `contacts-memorylink-differential` and
`contacts-security-differential`; `make contacts-dataset-differential` runs
all of them plus the dataset sync/purge/query/aggregate differentials. Build-volume
runners can override `CONTACTS_GO` and `CARGO` with their approved launchers.
Fixtures are registered in portgen; its shared provenance manifest must be
refreshed by the integration owner when adding these fixtures.

## Not complete

- The Rust crate is not wired into a CLI/MCP facade; Go remains production.
- Backup snapshots use SQLite `VACUUM INTO`; page-level layout of a Rust
  backup is not byte-identical to Go's (salt/nonce are random), only the
  format and restored contents are certified.
- Export reads TEXT columns; a NULL name column fails with Go's scan error,
  non-text storage classes in those columns are stringified.
- Concrete Rust environment/process/terminal key-provider adapters and
  real-TTY password/confirmation behavior need native verification.
- UUID/time defaults, stream I/O failures and non-UTF-8 CSV inputs need adapters.
- Planning currently uses per-row queries rather than Go's batched queries;
  batch-scale performance/concurrency are not certified.
- Driver-level SQLite error text/wrappers are not parity-certified; the checked
  import resolution errors and CSV parser errors are exact Go strings.
- Native Linux/Windows execution and CLI/facade integration remain pending.

No intentional externally visible divergence or Go fallback removal is approved
by this slice. The remaining limits are declared on CONTACT-001 in the contract matrix.
