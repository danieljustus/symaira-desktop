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
- Contact-point normalization: 36 Go-generated cases, including simple Unicode
  case mapping and non-ASCII decimal digits.
- Duplicate planning (exact source, normalized email/phone, SQLite NOCASE names)
  and explicit create/skip/merge application. Merge is additive; unresolved
  duplicates are skipped. Organization creation/membership, contact-point
  uniqueness, source linking and import-run counts are checked in ordered DB
  snapshots. Re-import outcomes are checked separately.

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
`contacts-schema-differential`, `contacts-import-differential` and
`contacts-vcard-differential`. Build-volume
runners can override `CONTACTS_GO` and `CARGO` with their approved launchers.
Fixtures are registered in portgen; its shared provenance manifest must be
refreshed by the integration owner when adding these fixtures.

## Not complete

- Person/organization CRUD, child-table service APIs, opaque-reference facade,
  relationships/follow-ups, keyprovider, backup and erase remain Go-only.
- UUID/time defaults, stream I/O failures and non-UTF-8 CSV inputs need adapters.
- Planning currently uses per-row queries rather than Go's batched queries;
  batch-scale performance/concurrency are not certified.
- Driver-level SQLite error text/wrappers are not parity-certified; the checked
  import resolution errors and CSV parser errors are exact Go strings.
- Native Linux/Windows execution and CLI/facade integration remain pending.

No intentional externally visible divergence or Go fallback removal is approved
by this slice. These limits must remain open in RUST-009 / CONTACT-001.
