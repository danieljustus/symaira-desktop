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
| `vault health` | done (vault health commit) |
| `relations contact [link|unlink]` | done (contact commit) |
| `doctor` | done (doctor commit) |
| `dataset list` | done (dataset commit) |
| `ai autofill` | done (ai commit) |
| `notebook generate` | done (ai commit) |
| `export --note|--view` | done (export commits) |
| `ingest jobs|retry|reocr|split|merge|rotate`, `mail *`, `rules *`, `clip`, `meeting *` | sibling r013-ingestcmds |
| `paperless import|migrate`, `views embed|exec|export-csv|import-csv|new-entry` | sibling r013-paperviews |

## Done

- perm user/group, vault adopt, demo init (see commits above); replay 8 tests / 15 cases pass.
- Fixture checksum + generator digest registered in `testdata/port/provenance.json`.
- relations contact (resolve by name / `--id`, link, unlink): 7 cases / 112 steps, 22 cases total.
  The Go recorder seeds the contact store below the isolated HOME (`contact_sql`: the Go process creates and
  migrates the store, the test runs INSERTs through the sqlite driver); the replay applies the same
  statements through `symdesk-contacts`. `symdesk-cli` now depends on `symdesk-contacts` (workspace dependency
  + Cargo.lock) and `symdesk_render::export_vault::{parse_file_in_root, read_in_root}` are `pub`.

## Remaining

- None known: every non-native path of `symdesk-command-tree.json` in this slice has a Go-recorded replay.

- doctor (text and `--json`): 10 cases / 22 steps (clean, archive paths, archive env, ASN problems, no vault,
  fake sibling tools through SYMAIRA_BIN / managed runtime / controlled PATH, backend down, AI provider,
  extra arguments). The host toolchain never shows through: PATH is a case directory, tools are scripts below
  the isolated HOME, the embedding backend is a loopback fake or a closed port. Go lists the text report in
  random map order, so the harness compares the sorted lines (`sort_lines`).
  `Resolver::inspect_tool` (symdesk-core) is the port of `compose.InspectTool`; `index_cli::retrieval_health`
  and `config_paths::ingest_archive_path` expose the retrieval snapshot and the ingest archive resolution.

- dataset list / describe (text and `--json`): 3 migration cases + arguments/empty/no-vault/`datasets`-is-a-file,
  7 cases / 32 steps. `dataset describe` now goes through the same legacy-policy migration as Go
  (`readDatasetHandleWithMigration`) and wraps its error as `dataset "x" not found: <cause>`.

- vault health (text and `--json`): 7 cases / 30 steps (clean, findings of every category, derived fresh/stale/
  orphaned with set mtimes, near-duplicate groups at several thresholds, arguments, empty vault, no vault).
  `vault_health_cli.rs` ports `health.Scan` on `HealthLinkResolver` / `MarkdownLinkResolver`.

- export --note (html, pdf, dataset export policy): 10 cases / 51 steps. HTML files are compared in the working
  directory snapshot; the dataset policy is exercised with the default, a widened, a narrowed and an invalid
  `SYMDESK_DATASET_EXPORT_MAX_SENSITIVITY`. `--output` is Cobra's local flag (argv rewrite like `search export`).

- export --view (html, csv, pdf-no-engine, dataset-sourced views and their policy): 4 cases / 28 steps, after merging
  main (r013-ingestcmds / r013-paperviews) into this branch for the saved-view engine (`views_exec_cli::{find_view,
  exec_view, view_csv, check_dataset_export}` are now `pub(crate)` and reused).

- ai autofill / notebook generate (text and `--json`): 8 cases / 62 steps against a loopback fake of Ollama's
  `/api/generate` (`$LLM_URL`; `adminLLMAnswer` in Go, `admin_llm_fake.rs` in the replay: same marker rules). Covers
  dry-run, apply, re-apply, skipped/empty/failed notes, provider errors, no provider, vault templates and the
  built-in kinds, artifact frontmatter and snapshots.

## Declared limits
- export --note: a *successful* PDF render needs the host's typst and writes a non-reproducible PDF, so only the
  no-engine error (empty PATH) is recorded; the render call is the one `search export` already uses. Go has no
  bound on `![[..]]` cycles (the Go recorder would never finish), so no cycle case is recorded.
- vault health: a YAML *syntax* error in frontmatter carries the yaml.v3 wording in Go (noyalib text in Rust), so
  the fixture records an invalid-ASN parse error instead. Findings are sorted stably; Go's `sort.Slice` is
  unstable above twelve findings, so the cases stay below that.

- dataset policy migration: Go ranges over a two-entry map, so it writes `sensitivity` / `retention_rule` in
  random order. The Rust port writes `sensitivity` first; the harness folds the swapped adjacent pair
  (`normalizeAdminText` / `normalize_text`, volatile cases) so the Go recorder is deterministic.

- doctor: `parse_errors[].message` for malformed frontmatter and `workspace_tools[].probe_error` for a
  failing/non-JSON `version --json` probe carry the Go yaml.v3 / `os/exec` / `encoding/json` wording; the
  Rust text differs, so neither is recorded (the Go fixture has no malformed frontmatter and every fake
  tool answers correctly). `ai.secret_source` for an empty key asks the host Keychain (`security`), so the
  cases always configure a key. A non-empty `agent_results` directory is not recorded (it lives under a
  hashed data directory).

- relations contact: the frontmatter of a note that is rewritten (link, unlink, `--id` cache refresh) is
  re-encoded through the shared Go-yaml renderer (`render_go_yaml_map`). Unquoted YAML timestamps in
  rewritten frontmatter are rendered as quoted strings (Go keeps `time.Time` plain), and nested maps whose
  key set is exactly a Property/View/Filter shape get that struct's field order instead of sorted keys.
  Typed meeting-frontmatter decoding is approximated by scalar/sequence/mapping shape checks; a meeting that
  fails them is skipped as in Go, but exotic yaml.v3 coercions are not reproduced. The on-disk-changed
  conflict guard is implemented but not exercised (two reads of one instant).
- Gate: `scripts/rust-port/local-gate.sh` on both hosts.
