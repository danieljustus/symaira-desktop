# RUST-013 slice r013-ingestcmds (resume note)

Branch `local/r013-ingestcmds` (base `290bc4d2`). Native Rust bodies for the
ingest/mail/rules/meeting/clip command families, differential-tested against the
Go process via `testdata/port/cli/cli-ingest-bodies-process.json`.

- Generate: `make cli-ingest-bodies-fixtures-generate` (Go generators
  `cmd/symdesk/ingest_bodies_*port_test.go`, loopback fakes only).
- Check: `make cli-ingest-bodies-differential` (Go check + Rust replay
  `crates/symdesk-cli/tests/cli_ingest_bodies.rs`).
- Registered in `scripts/rust-port/cmd/portgen/{main,fixture_checks}.go` and
  `testdata/port/provenance.json` (fixture checksum + `generator_source_digest`).
  Re-freeze the digest after every commit that touches a generator source: the
  digest is computed from `HEAD`, so commit first, run `make port-fixtures-check`,
  copy `checked=` into `generator_source_digest`, commit as
  `testdata(port): re-freeze the generator digest`.

## Done

- `rules *`, `mail rules *` (1ce796b6)
- `ingest`, `jobs`, `retry`, `reocr`, `split`, `merge`, `rotate` (2ca771d0)
- `mail status`, `mail fetch` (8cd0bb0a)
- `clip` against a fake symbrowse (a41ee0d8)
- fixture checksum registered in `testdata/port/provenance.json`
- `meeting list|show|review|publish` and `meeting participant
  candidates|confirm|create|unlink-contact` (`crates/symdesk-cli/src/meeting_cli.rs`,
  Go cases `cmd/symdesk/ingest_bodies_meeting_port_test.go`; stateful fake
  `symmemory` below the case HOME `mem/`). The frontmatter is re-encoded from
  Go's typed `meetingFrontmatter` shape (declaration order + inline extras) via
  `render_go_yaml_meeting_map` (thread-local struct hints in
  `crates/symdesk-vault/src/mutations/go_yaml.rs`).

## Remaining

- `meeting participant contact|link-contact` and `relations contact [link|unlink]`
  need the in-process contact store (`internal/contacts`, SQLite under
  `XDG_DATA_HOME`); the Rust CLI does not open it yet (`symdesk-contacts` has
  `open`/CRUD). The Go recorder cannot import `internal/contacts/internal/*`,
  so seed the store by SQL or through the real CLI.
- Other non-native paths from `testdata/port/cli/symdesk-command-tree.json`:
  `demo init`, `doctor`, `paperless import|migrate`, `perm user|group
  add|remove|token`, `vault adopt`, `views embed|exec|export-csv|import-csv|
  new-entry`. Verify each against `crates/symdesk-cli/src/main.rs` first.

## Declared limits (meeting)

- Nested YAML timestamps inside extras (below a top-level key) are strings in
  the Rust parser, Go re-encodes them as plain `time.Time`; only top-level
  timestamps are converted.
- `1_000`-style YAML 1.1 underscore integers are strings in noyalib (Go: int).
- `meeting show` text prints the mtime in UTC (or the host zone) with the
  sub-second part normalized; a non-nil `ASN` prints a pointer in Go.
- yaml.v3 decode-error line numbers are reproduced for top-level fields and
  `symmeet_source.artifact_schema_version`; deeper mismatches use the line of
  the owning top-level key.
- The published-fact ledger order comes from Go map iteration; the recorders
  compare it sorted.
