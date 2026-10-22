# SymRoom gap inventory (RUST-016, #1145)

Measured against the Go `symroom` source at `a4159162` and the committed Rust
tests/fixtures, after the I/O-error slice below. A "gap" is behaviour the Go
process shows that no Go-owned fixture pins and the Rust port has not been
proven to match. Counts come from probes run against the built Go and Rust
binaries in disposable rooms; probe-only counts are labelled as such and are
not committed evidence until a fixture exists.

## Implemented in this slice

`testdata/port/room/io-error-cli.json` (79 Go-recorded cases, Go test
`TestPortIOErrorCLIContract`, Rust replay `crates/symroom-cli/tests/io_error_commands.rs`,
`make room-io-error-cli-differential`) pins the text, exit code and file effects
of a command that fails on the filesystem: missing, directory-instead-of-file,
file-instead-of-directory and unreadable `room.toml`/`journal`, across
`run list|show|request|start|cancel|approve|deny`, `checkpoint resolve`, `note`,
`decide`, `member list|remove|role`, `artifact link|unlink|list`, `verify`,
`log`, `index rebuild` and `doctor`. The host wording of an OS error is the
tokens `<ENOENT>`, `<ENOTDIR>`, `<EISDIR>` and the work directory is `<work>`,
so one fixture serves every native OS. Rust gains `symroom_core::goio`
(`filepath.Clean` display, `*fs.PathError` text) and uses it where Go surfaces a
raw `os` error (`room.toml`, journal directory/segments, artifact hashing, MCP).

## Rows

| Row | Missing | Implementable now | Go reference | Notes |
|---|---|---|---|---|
| ROOMCLI-000 | 0 local; Linux/Windows native receipts | no (CI) | `cmd/symroom/cmd_version.go:14-19` | Only native CI evidence is outstanding. |
| ROOMCLI-001 | Flag grammar: 104 probe divergences in 5 classes (below); aggregate #1153; Linux receipt | yes | `cmd/symroom/main.go:65` (dispatch); one `flag.NewFlagSet(..., ExitOnError)` + `fs.Parse` per command, e.g. `cmd_run.go:26-31,48-51,70-72,107-109,130-133,154-157,191-195,216-219`, `cmd_member.go:30-36,62-64,110-112,128-130`, `cmd_artifact.go:23-26,48-50,68-70`, `cmd_checkpoint.go:25-30,64-67`, `cmd_note.go:16-19`, `cmd_decide.go:17-21`, `cmd_verify.go:16-18`, `cmd_log.go:15-23`, `cmd_index.go`, `cmd_identity.go:62-64`, `cmd_init.go:15-18`, `cmd_doctor.go:16-18`, `cmd_watch.go:20-23`, `cmd_brainprofile.go:14-17`, `cmd_mcp.go:15-19`, `cmd_version.go:14-16` | The 122-case grammar fixture covers usage, `--help` and unknown `-flag`; the classes below are not in it. |
| ROOMCLI-002 | `index rebuild` success path prints the host separator (`.symroom\index.sqlite` on Windows); `identity create` prints a random member id (shape only); native Windows replay of the new I/O fixture | partly (separator/random id are native-only or shape-only) | `cmd/symroom/cmd_index.go`, `cmd_identity.go` | `run list` and every other missing-`room.toml` path is now pinned (79 cases). |
| ROOM-001 | The `symvault` shell-out and macOS Keychain `security` chains have no Go-owned fixture | yes (fake `symvault`/`security` on a scoped PATH) | `internal/room/identity/identity.go:100` (`Load`), `:122` symvault, `:141` `security find-generic-password` | Rust implements both chains (`crates/symroom-core/src/identity.rs:197-240`); only the file/env paths are replayed. The matrix text "the journal remain open" is stale: ROOM-002 owns it. |
| ROOM-002 | Exact-current native Linux/macOS/Windows matrix | no (CI) | `internal/room/journal/journal.go:224-259,302`, `merge.go:31`, `verifier.go:36` | Harness note: `index_commands` and `index_contracts` need a TMPDIR whose ancestors are not group-writable; they fail under `dev-external`'s `.worktrees/*/tmp` and pass with a private TMPDIR. Not a product gap. |
| ROOM-003 | `run.Finish`, `run.Fail`, `run.ExecuteAdapter` have no Rust port; exact native matrix | yes, library level only | `internal/room/run/run.go:182` (`Finish`), `:228` (`Fail`), `internal/room/run/adapter.go:22` (`ExecuteAdapter`, fails the run via `Fail` at `:105`) | No Go CLI or MCP path calls them (only `adapter_test.go`), so there is no process fixture to replay; the projection of `run.finished`/`run.failed` is already covered. |
| ROOM-004 | Native Windows replay; exact native matrix | no (CI) | `internal/room/artifact/artifact.go:35,66,124,153,216`, `internal/room/desk/watch.go:21,51` | No local gap found; I/O hashing errors are now pinned by the new fixture. |
| MCP-004 | Native Linux/macOS/Windows replay; 12 `/bin/sh` scenarios are Unix-only; in-flight cap 512 vs Go's unbounded goroutines (observable only above 512 concurrent calls) | no (CI; cap is a recorded deviation) | `cmd/symroom/cmd_mcp.go:15-19`; `crates/symroom-cli/src/mcp.rs:40` | MCP file errors now use the same Go-shaped text (`goio`). |

### ROOMCLI-001 flag-grammar divergences (probe, `room`-less state)

Go uses the standard `flag` package; the Rust hand-parsers differ for these
argv shapes (104 of the permutations probed across 29 command paths):

| Class | Count | Go | Rust |
|---|---|---|---|
| `--flag=value` on an undefined flag | 52 | `flag provided but not defined: -identity` | `... -identity=a` (value kept in the name) |
| lone `-` argument | 36 | positional; command proceeds | `flag provided but not defined: -` |
| `--json=false/1/x` on a defined bool flag (`version`) | 6 | accepted or `invalid boolean value "x" for -json: parse error` | `flag provided but not defined: -json=...` |
| `--json=...` / `--identity=a` on `identity export` | 6 | `flag provided but not defined: -json` | name includes `=value` |
| `--` terminator | 4 | ends flags; command proceeds | `flag provided but not defined: -` |

Fixing this needs one Go-`flag`-compatible parser shared by the ~16 Rust
command files plus a Go-owned fixture; that is the next-largest gap and is
larger than this slice.

## Evidence for this slice

See the commit and `docs/rust-port/contract-matrix.md` (ROOMCLI-002). The
negative control (mutating `goio::path_error_text` to print the raw path) makes
43 of 79 cases fail; restoring it passes.
