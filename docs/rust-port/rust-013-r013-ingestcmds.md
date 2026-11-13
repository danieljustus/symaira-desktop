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
- `meeting participant contact|link-contact` against the in-process contact
  store (`symdesk-contacts` `open`, store paths from
  `symdesk_core::config::contacts_paths`). Cases may seed the store
  (`contacts` in the fixture): both harnesses let their own binary create and
  migrate the store, then insert the rows with SQL.

## Remaining

- `relations contact [--id] <name|id>`, `relations contact link|unlink`
  (Go `cmd/symdesk/relations.go`, `Service.ResolveContactReferences*`,
  `LinkNoteContact`, `UnlinkNoteContact`): not an ingest/mail/rules/meeting
  path, still without a native body. The contact-store plumbing for it exists
  in `meeting_cli.rs` (`open_contact_store`, `resolve_contact_ref`).
- Other non-native paths from `testdata/port/cli/symdesk-command-tree.json`
  that are outside this slice: `demo init`, `doctor`, `paperless
  import|migrate`, `perm user|group add|remove|token`, `vault adopt`,
  `views embed|exec|export-csv|import-csv|new-entry`.
- Every ingest, mail, rules, meeting and clip command path of the tree is now
  native and has recorded cases (`consume` is covered by `cli-bodies-process`).

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
