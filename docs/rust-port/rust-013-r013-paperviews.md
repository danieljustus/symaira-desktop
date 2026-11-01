# RUST-013 slice r013-paperviews

Native Rust bodies for `paperless import`, `paperless migrate` and
`views exec|new-entry|export-csv|import-csv|embed`, replayed against real Go
`symdesk` process recordings.

- Worktree/branch: `Worktrees/symaira-desktop/r013-paperviews`, `local/r013-paperviews` (base `290bc4d2`).
- Oracle fixture: `testdata/port/cli/cli-paperviews-bodies-process.json`
  (15 cases / 457 steps / 149 non-zero exits; every step also records the vault tree).
  Generator: `cmd/symdesk/paperviews_bodies_process_port_test.go`
  (`make cli-paperviews-bodies-fixtures-generate`). Replay:
  `crates/symdesk-cli/tests/cli_paperviews_bodies.rs`
  (`make cli-paperviews-bodies-differential`).
- `paperless migrate` talks to a loopback fake Paperless-ngx
  (`tests/support/paperless_fake.rs`); the requests the fake served are
  compared per step.

## Done

- Replay green: 457/457 steps equal (stdout, stderr, exit, vault tree, fake requests).
- Go parity fixes found by the replay: trailing-colon YAML scalars are
  single-quoted (libyaml block indicator), last-wins duplicate manifest keys,
  Go-named wrong top-level manifest type, `filter.value` optional, `fmt`
  sorted nested map keys in `views exec` rows, missing archive destination
  directory is non-fatal on import, archive path is `filepath.Join`-cleaned,
  dataset export-sensitivity policy for `views export-csv`, Go wording for
  dataset-handle/`new-entry` write/CSV-directory errors, embed duplicate-key
  and type-error line numbers / short scalars.

- portgen manifest/check target and provenance checksum registered, generator
  digest re-frozen; `make port-fixtures-check` PASS (commit `127fa68a`).

## Remaining

- Promote the decision-only `paperless import`, `views new-entry`, `views exec`
  steps of `remote-command-process.json` to exact.
- Two-host gate.

## Declared limits

- YAML *syntax*-error wording of `views embed` (libyaml vs noyalib) is not
  replayed (the `bad.yaml` case is not recorded); type errors and duplicate
  keys are.
- `paperless migrate` connection-refused / invalid-URL wording (Go `net/http`)
  and wrong-element-type manifests (Go decoder wording) are not recorded.
