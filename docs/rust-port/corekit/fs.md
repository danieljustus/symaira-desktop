# CoreKit adoption: filesystem (`symaira-core-fs`), CK-FS

Pinned CoreKit revision: `592bebb54080d6b37372e47c461cfa3aeca7d2af`
(`rust/symaira-core-fs/src/lib.rs`). Base: `f24377a4`.

**Result: no site adopted, no dependency added.** At every place where the
removed Go backend used CoreKit `fsutil`, the Rust crate is observably different
from the Go behavior that the frozen fixtures record. Replacing the local code
would change temp-file naming, error text, or accepted inputs. The local code
stays, and the differences are recorded below as upstream gaps.

## Go use sites mapped to Rust successors

Go reference: `git show 36aad3d4:<path>`. The Go library behind them is
`fsutil/atomicwrite.go` `writeTempThenRename` at the corekit version in the Go
`go.mod` (`27d2daf8ec1e`): `os.CreateTemp(dir, base+".tmp-*")`, `Chmod(perm)`,
write, `Sync`, `Close`, `rename`; raw `*PathError` strings; no parent-directory
inspection. `fsutil.SafeMkdirAll` (`fsutil/safewrite.go:99`) is `Lstat` per
component plus `os.Mkdir(cur, perm)`; only symlinks not owned by root and
non-directories are rejected; errors are raw `*PathError`.

| Go call (36aad3d4) | fsutil fn, mode | Rust successor | Decision |
|---|---|---|---|
| `annotate/writer.go:44` | AtomicWriteFile, 0600 | `symdesk-ingest/src/annotate.rs:272` -> `writer.rs:158 atomic_write_file` | kept local |
| `config/mail_contract.go:237` | AtomicWriteFile, caller mode | `symdesk-cli/src/mail_rules_cli.rs:473` -> `writer::atomic_write_file` | kept local |
| `ingest/watcher.go:459` | AtomicWriteFile, 0600 (`.error.json`) | `symdesk-ingest/src/watcher.rs:294` -> `atomic_write_file` | kept local |
| `notionimport/import.go:298` | AtomicWriteFile, 0600 | `symdesk-ingest/src/writer.rs:498` -> `atomic_write_file` | kept local |
| `writer/writer.go:188, 327` | AtomicWriteFile, 0600 | `symdesk-ingest/src/writer.rs:623, 498` -> `atomic_write_file` | kept local |
| `room/room.go:61, 97, 128` | SafeMkdirAll, 0755 | `symroom-core/src/room_init.rs:69-73, create_dir` | kept local |
| `room/room.go:105, 122, 140, 148` | AtomicWriteFile, 0644 | `symroom-core/src/room_init.rs write_new` (create_new + chmod, no temp file) | kept local |
| `ingest` archive copy (writer/pipeline) | stream copy through own temp | `symdesk-ingest/src/pipeline.rs:722 atomic_copy` | kept local (no fsutil call in Go; CoreKit takes `&[u8]`, not a reader) |

## Why `write_file_atomic` is not identical to `atomic_write_file`

Measured with a throwaway probe (deleted; source kept in the build logs dir as
`zz_fs_probe.rs.txt`) that called `symdesk_ingest::writer::atomic_write_file`
and `symaira_core_fs::write_file_atomic` on identical sandboxes:

| Scenario | Local (Go-parity) | CoreKit `write_file_atomic` |
|---|---|---|
| success / overwrite, mode 0644 | `Ok`, `a.md:0644`, no leftovers | `Ok`, `a.md:0644`, no leftovers (same) |
| rename target is a directory | `Err("rename <dir>/d.tmp-3085123969 <dir>/d: is a directory")` | `Err(Atomic { path, source: IsADirectory })`, text `atomic operation failed for <dir>/d: Is a directory` |
| 250-char file name | `Err("open <dir>/nnn....tmp-3570443021: file name too long")` | `Err(Atomic { .. "File name too long" })`, no temp name in text |
| missing parent | `Err("open <dir>/nope/a.tmp-1359617464: no such file or directory")` | `Err(Io("No such file or directory"))`, no path |
| parent mode 0777 (owner, no sticky bit) | `Ok`, file written | `Err(InsecureDirectory)`, nothing written |
| parent reached through a symlink | `Ok`, file written | `Err(Io(NotADirectory))`, nothing written |

Source of each difference:

- Temp name: CoreKit `lib.rs:800-807 temp_name` makes `.{name}.tmp-{nanos:hex}-{n:hex}`
  (hidden, hex, counter). Go and local `writer.rs:158-195` make
  `{name}.tmp-{digits}`. The frozen fixture records the Go text:
  `testdata/port/ingest/pipeline.json:12121`
  (`write note: write note: open <ROOT>/vault/nnn...txt.md.tmp-N: file name too long`)
  and the tests normalize `\.tmp-\d+` (`crates/symdesk-ingest/tests/pipeline_contracts.rs:102,333`,
  `paperless_run_contracts.rs:214`, `paperless_verify_contracts.rs:102`,
  `symdesk-cli/tests/cli_paperviews_bodies.rs:364`). A CoreKit temp name does not
  match, and the error text `atomic operation failed for ...` replaces the
  `open <tmp>: <errno text>` form that the CLI/HTTP/MCP surfaces print.
- Parent policy: CoreKit `lib.rs:771-798 secure_parent` plus `lib.rs:519-522
  is_secure_handle` open every ancestor with `O_NOFOLLOW` and reject directories
  that are group/world-writable or not owned by the euid or root. Go never
  inspected parents. Real vaults on shared or external volumes (mode 0777 mount
  roots, symlinked vault paths) write fine today and would fail.
- Also CoreKit fsyncs the parent directory after rename (`lib.rs:424`); not
  observable in fixtures, but extra I/O that Go did not do.

An error-mapping adapter cannot repair this: the temp name and the errno text
are produced inside CoreKit, and the secure-parent rejection is an acceptance
difference, not a text difference.

## Why `safe_mkdir_all` does not replace the room directory code

- `lib.rs:182-253` creates with `mkdirat` and raw `FsError::Io` (no `mkdir
  <path>:` prefix; local `room_init.rs:69-73` rebuilds Go's `mkdir <path>:` text
  through `goio::path_error`), and rejects insecure existing ancestors (same
  group/world-writable rule as above) which Go `SafeMkdirAll` did not.
- Room init today does not use temp files at all (`write_new`, create_new +
  chmod). Switching to `write_file_atomic` would be a behavior change beyond
  adoption and would inherit the gaps above. The fixture
  `testdata/port/room/init.json` (bytes and modes 0644/0755) passes with the
  current code; CoreKit would pass it too on success, but the failure behavior
  above is not covered by the fixture and differs from Go.

## Secondary sites (Go did not use fsutil)

Not adopted. Each has its own temp-name scheme and typed error that is visible
or contract-bound, so none is provably identical to CoreKit:

- `symdesk-vault/src/mutations.rs:361 write_atomic`: `.symdesk-frontmatter-{pid}-{n}.tmp`, `MutationError::{TempCreate,TempWrite,TempSync,TempRename}`.
- `symdesk-protocol/src/lib.rs:5780 write_atomic_root`: cap-std rooted, `.symdesk-http-put-{pid}-{n}.tmp`, caller mode (cap-std confinement was deferred in `corekit-adoption.md`, FS slice).
- `symdesk-cli/src/views_exec_cli.rs:931 write_atomic_private`: `.symdesk-csv-{pid}{n}.tmp`, error strings `create temp file:` / `write temp file:` / `rename temp file:`.
- `symdesk-core/src/pre_rust_backup.rs:406 atomic_write`: `{path}.tmp-{pid}`, no chmod, `io::Result`.
- `symdesk-index/src/dataset_sync.rs:1090 write_atomic`: cap-std `Dir`, `.symdesk-dataset-{pid}-{n}.tmp` (deferred cap-std reasons still hold).

The earlier cap-std deferrals (`symdesk-index`, `symdesk-protocol`,
`symdesk-vault/src/paths.rs`) were not revisited; the reasons still hold.

## Upstream gaps (CoreKit `symaira-core-fs`)

1. Temp-file naming is not configurable: `temp_name` (`lib.rs:800-807`) vs Go
   `os.CreateTemp(dir, base+".tmp-*")`. Needs an option or a `_with_temp_pattern`
   variant to keep `{base}.tmp-{digits}` and the Go error text.
2. Errors lose the Go `op path: errno` form (`FsError::Atomic` at `lib.rs:405-423)
   438-440`, `FsError::Io` for mkdir/missing parent). A Go-compatible error
   rendering is needed to keep CLI/HTTP/MCP strings.
3. `secure_parent` / `is_secure_handle` (`lib.rs:519-522, 771-798`) reject
   group/world-writable and symlinked ancestors; Go accepted both. A non-strict
   mode (or documented policy decision recorded in the contract matrix) is needed
   before these helpers can replace Go-parity writers.
4. No streaming variant (`write_file_atomic` takes `&[u8]`) for the archive copy
   in `pipeline.rs:722`, and no "keep existing destination" mode.

## Checks

- `dev-external --status`: healthy, mounted.
- Probe: cold build + run of the throwaway test, 1 test executed, output above.
- Worktree after cleanup: only this doc is added; `Cargo.toml`, `Cargo.lock` and
  crate manifests are unchanged, so no crate gates are affected. Workspace gate:
  pending (coordinator).
