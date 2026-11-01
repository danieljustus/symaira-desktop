# RUST-013 slice r013-admin

Branch `local/r013-admin` (base 290bc4d2). Administrative command bodies the Rust CLI did not yet run
after the Cobra front half. Fixture `testdata/port/cli/cli-admin-bodies-process.json` is recorded by
`cmd/symdesk/admin_bodies_*port_test.go` (`TestPortCLIAdminBodiesProcessContract`) from the real Go
`symdesk` process in an isolated HOME/XDG/cwd; the Rust replay is
`crates/symdesk-cli/tests/cli_admin_bodies.rs`. Targets: `make cli-admin-bodies-fixtures-generate`,
`make cli-admin-bodies-differential`.

## Inventory (non-native paths of `symdesk-command-tree.json`)

Probed on the built binary (every runnable path, with dummy arguments for the Cobra arity/required-flag
checks): a path is non-native when clap rejects it (`unrecognized subcommand`) or it falls through to the
silent `_ => Ok` arm.

| Path | Owner |
|---|---|
| `perm user|group ...` | done (7f79cb00) |
| `vault adopt` | done (ca07d32d) |
| `demo init` | done (88261163) |
| `vault health` | this slice |
| `relations contact [link|unlink]` | this slice |
| `doctor` | this slice |
| `dataset list` | this slice |
| `ai autofill` | this slice |
| `notebook generate` | this slice |
| `export` (group node) | this slice (verify Go behavior) |
| `ingest jobs|retry|reocr|split|merge|rotate`, `mail *`, `rules *`, `clip`, `meeting *` | sibling r013-ingestcmds |
| `paperless import|migrate`, `views embed|exec|export-csv|import-csv|new-entry` | sibling r013-paperviews |

## Done

- perm user/group, vault adopt, demo init (see commits above); replay 8 tests / 15 cases pass.
- Fixture checksum + generator digest registered in `testdata/port/provenance.json`.

## Remaining

- vault health, relations contact, doctor, dataset list, ai autofill, notebook generate, export.
- Gate: `scripts/rust-port/local-gate.sh` on both hosts.
