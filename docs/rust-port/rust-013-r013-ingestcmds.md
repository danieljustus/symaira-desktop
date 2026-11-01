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
- fixture checksum registered in provenance.json

## Remaining

See the commit log of this branch for later entries; this file is updated with
every commit.
