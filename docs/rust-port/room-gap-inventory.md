# SymRoom gap inventory (RUST-016, #1145)

Measured against the Go `symroom` source at `a4159162` and the committed Rust
tests/fixtures, after the I/O-error slice below. A "gap" is behaviour the Go
process shows that no Go-owned fixture pins and the Rust port has not been
proven to match. Counts come from probes run against the built Go and Rust
binaries in disposable rooms; probe-only counts are labelled as such and are
not committed evidence until a fixture exists.

## Implemented in this slice

`testdata/port/room/io-error-cli.json` (143 Go-recorded cases, Go test
`TestPortIOErrorCLIContract`, Rust replay `crates/symroom-cli/tests/io_error_commands.rs`,
`make room-io-error-cli-differential`) pins the text, exit code and file effects
of a command that fails on the filesystem: missing, directory-instead-of-file,
file-instead-of-directory and unreadable (`segperm`: mode 000 segment)
`room.toml`/`journal`, across `run list|show|request|start|cancel|approve|deny`,
`checkpoint resolve`, `note`, `decide`, `member list|remove|role`,
`artifact link|unlink|list`, `verify`, `log`, `index rebuild` and `doctor`. It
also pins Go's lenient `room.toml` scanner (`toml-garbage`, `toml-noid`,
`toml-lenient`: malformed, id-less and duplicate-id files are not errors; the
resolved room id is recorded per event in `rooms`) and `$PWD` handling in
`filepath.Abs` (`pwd-link`: the doctor prints the symlink name Go's `os.Getwd`
returns).

Rust gains `symroom_core::goio` (a string port of `filepath.Clean` with Go's
Windows volume rules, `os.Getwd`'s `$PWD` rule, `*fs.PathError` text),
`symroom_core::room_config` (the single lenient `ReadRoomConfig` scanner shared
by run/approval/member/note/decide/MCP) and the `read segment <author>:`
wrapper of `readAllSegmentsUnlocked`.

### Windows handling (no single fixture for every OS)

A case is recorded on Unix. The fixture's tokens (`<ENOENT>`, `<ENOTDIR>`,
`<EISDIR>`, `<EACCES>`, `<work>`, `<link>`, masked `<ev_id>`/`<chk_id>`) cover the
OS wording, but not the *semantics*: on Windows a missing parent directory or a
file used as a directory is `ERROR_PATH_NOT_FOUND`, which Go's `syscall.Errno`
also reports as "does not exist", so Go itself takes different code paths
(`os.IsNotExist` branches, a journal that is a file can read as empty) and
prints different text. Those expectations cannot be derived from the Go source
without a Windows run, so each case carries `unix_only` and:

- Go (`TestPortIOErrorCLIContract`) and Rust replay only the non-`unix_only`
  cases on Windows (states `empty`, `tomldir`, `artdir`, `toml-*`: 66 cases);
  the other 77 (`nope`, `filedir`, `journalfile`, `abs-journalfile`, `segperm`,
  `pwd-link`) are logged `SKIP <name>: <reason>` and counted on both sides
  (`I/O error cases: N total, M replayed, K SKIPPED`), and a run that skips
  everything fails. On Windows the Go test substitutes the committed case for a
  skipped one, so the whole-file byte comparison stays exact for the rest.
- `segperm` is also skipped on Unix when the user can read a mode 000 file
  (root), probed at run time.
- Generation (`PORT_GENERATE=1`) refuses to run on a host that would skip.

Residual: the Windows subset has been derived, not run natively; native
Windows AMD64/ARM64 CI is the first real receipt. `filepath.Abs` on Windows is
`GetFullPathName`, not `Clean(Getwd()+path)`; `goio::abs` models the Unix rule
only (the Windows doctor path is not in the replayed subset).

### Append-path wrapper divergences (probe-only, not fixture-pinned)

Probed against the built Go and Rust binaries (168 cases over `segdir`,
`segdir-other`, `journal-noperm`, `journal-xonly`, `toml-noperm`,
`bad-utf8-toml`, `good+emptyjournal`; 9 differ), all on commands that append:

| Divergence | Go | Rust |
|---|---|---|
| segment file is a directory, `run request`/`checkpoint request`/`artifact unlink` (`Journal.Append`) | `open segment file: open journal/<seg>.jsonl: is a directory` | `open journal/<seg>.jsonl: is a directory` |
| same, `note`/`decide` (`room.AppendEvent`) | `append event: open journal file: open ...` | `open journal/<seg>.jsonl: is a directory` (no `append event: open journal file:` wrappers) |
| journal directory not listable (mode `x`), appends | `PrepareEvent` ignores the list error and fails at `open segment file: ... permission denied` | `read journal dir: open journal: permission denied` |

Cause: Go's `tailStatForLocked`/`PrepareEvent` ignore errors that Rust's
`author_stats` reports, and the two Go append functions wrap differently
(`room.AppendEvent` `open journal file:`/`mkdir journal:`, `Journal.Append`
`open segment file:`/`mkdir journal dir:`) while Rust has one `append_event`.
Not trivial: matching needs the tail-stat error semantics ported and a
per-caller append function, plus a `segdir` state; left as a documented
residual.

## Rows

| Row | Missing | Implementable now | Go reference | Notes |
|---|---|---|---|---|
| ROOMCLI-000 | 0 local; Linux/Windows native receipts | no (CI) | `cmd/symroom/cmd_version.go:14-19` | Only native CI evidence is outstanding. |
| ROOMCLI-001 | Closed locally in slice 4: non-UTF-8 argv is lossless and the `%q` non-printable escaping is ported; successful `run wait` is covered by `room-run-wait-cli-differential`, successful `identity create`/`index rebuild` by `store-cli.json`. Native Linux/Windows receipts and the aggregate #1153 remain | no (CI) | `cmd/symroom/main.go:65` (dispatch); one `flag.NewFlagSet(..., ExitOnError)` + `fs.Parse` per command, e.g. `cmd_run.go:26-31,48-51,70-72,107-109,130-133,154-157,191-195,216-219`, `cmd_member.go:30-36,62-64,110-112,128-130`, `cmd_artifact.go:23-26,48-50,68-70`, `cmd_checkpoint.go:25-30,64-67`, `cmd_note.go:16-19`, `cmd_decide.go:17-21`, `cmd_verify.go:16-18`, `cmd_log.go:15-23`, `cmd_identity.go:62-64`, `cmd_init.go:15-18`, `cmd_doctor.go:16-18`, `cmd_watch.go:20-23`, `cmd_brainprofile.go:14-17`, `cmd_mcp.go:15-19`, `cmd_version.go:14-16` | Closed locally by `testdata/port/room/flag-grammar-cli.json` (2231 Go-recorded process results, `make room-flag-grammar-cli-differential`); every command parser now goes through `crates/symroom-cli/src/goflag.rs`. Slice 4 adds the `bytes-*` cases of `testdata/port/room/store-cli.json`. |
| ROOMCLI-002 | Closed locally in slice 4 (`testdata/port/room/store-cli.json`, 131 cases): `index rebuild` success output and arbitrary `identity create` arguments. Native Windows replay of the I/O and store fixtures remains | no (CI) | `cmd/symroom/cmd_index.go`, `cmd_identity.go` | `run list` and every other missing-`room.toml` path is now pinned (79 cases). Random member ids and keys are compared by shape and consistency. |
| ROOM-001 | Closed locally in slice 4: 64-byte-key signing now uses the stored public half like Go (`sign-*` cases in `store-cli.json`); non-UTF-8 identity names are lossless. Native Linux/Windows replay of the identity fixtures remains | no (CI) | `internal/room/identity/identity.go:100` (`Load`), `:122` symvault, `:141` `security find-generic-password` | Closed locally in slice 3: `testdata/port/room/identity-provider-cli.json` (Go test `TestPortIdentityProviderCLIContract`, Rust `identity_provider_commands.rs`). Found and fixed two Rust divergences (public-half consistency check, `serde_json` identity-file decoding). The former documented signing deviation no longer exists. |
| ROOM-002 | Exact-current native Linux/macOS/Windows matrix | no (CI) | `internal/room/journal/journal.go:224-259,302`, `merge.go:31`, `verifier.go:36` | Harness note: `index_commands` and `index_contracts` need a TMPDIR whose ancestors are not group-writable; they fail under `dev-external`'s `.worktrees/*/tmp` and pass with a private TMPDIR. Not a product gap. |
| ROOM-003 | `run.Finish`, `run.Fail`, `run.ExecuteAdapter` have no Rust port; exact native matrix | yes, library level only | `internal/room/run/run.go:182` (`Finish`), `:228` (`Fail`), `internal/room/run/adapter.go:22` (`ExecuteAdapter`, fails the run via `Fail` at `:105`) | No Go CLI or MCP path calls them (only `adapter_test.go`), so there is no process fixture to replay; the projection of `run.finished`/`run.failed` is already covered. |
| ROOM-004 | Native Windows replay; exact native matrix | no (CI) | `internal/room/artifact/artifact.go:35,66,124,153,216`, `internal/room/desk/watch.go:21,51` | No local gap found; I/O hashing errors are now pinned by the new fixture. |
| MCP-004 | Closed locally in slice 4 with one accepted, tested difference: Rust runs at most 512 `tools/call` handlers and queues the rest, Go starts an unbounded goroutine per call (a resource hazard, observable only above 512 concurrent calls: a non-wait call sent after such a flood is answered after a worker frees). Native Windows replay remains; 12 `/bin/sh` scenarios and the POSIX process cases are Unix-only | no (CI; the cap is a recorded, tested deviation) | `cmd/symroom/cmd_mcp.go:15-19`; `crates/symroom-cli/src/mcp.rs:40` | MCP file errors now use the same Go-shaped text (`goio`). `testdata/port/room/mcp-process.json` pins 100 and 600 concurrent waits. |

### ROOMCLI-001 flag grammar (closed locally)

Go parses every command with the standard `flag` package (`flag.ExitOnError`);
the hand-written Rust parsers differed in 104 probed argv shapes in 5 classes
(`--flag=value` on an undefined flag kept the value in the name, 52; a lone `-`
treated as a flag, 36; `--json=<bool>` forms on `version`, 6; `identity export`
with `--json=`/`--identity=`, 6; the `--` terminator, 4). Every Rust command now
parses through one port of `FlagSet.parseOne` (`goflag.rs`):

- parsing stops at the first non-flag (`""`, a lone `-`, anything not starting
  with `-`); a later flag is positional; `--` ends the flags and is consumed;
- `-x`/`--x` are equivalent, `---x`, `-=x`, `--=x` are `bad flag syntax`;
- `-name=value` splits at the first `=`, the name of an undefined flag excludes
  the value; a bool takes no separate value and `=value` goes through
  `strconv.ParseBool`; any other flag takes the next argument, even `--`/`-`;
- ints use `strconv.ParseInt(s, 0, 64)` (base prefixes, underscore rule,
  `parse error` vs `value out of range`), durations `time.ParseDuration`;
  messages quote values with `%q`;
- `-h`/`-help` (any `=value`) that is not a defined flag prints the usage and
  exits 0; every other failure prints the message and the usage and exits 2.

`testdata/port/room/flag-grammar-cli.json` is derived from
`symroom-parser-grammar.json`: for each of the 43 command paths it replays the
shapes above plus every flag's type-specific value forms (bool: `=x`, `=`,
`=T`, `=TRUE`, `=0`, `=f`, `--b false`, repeated; int: `=0x10`, `=1_0`, `=+3`,
`=010`, `=1.5`, an overflow; duration: `=1h30m`, `=-5s`, `=5`; string: `=`,
`--s --`, `--s -`, repeated) in an empty directory and in a room (2231 cases,
47 of them Unix-only because they print a host OS error). The Go test records
exit code, both streams and the files and journal rooms left behind; the Rust
replay compares all five. Generation was run twice with Go 1.26.10 and
produced byte-identical output.

## Evidence for this slice

See the commits and `docs/rust-port/contract-matrix.md` (ROOMCLI-002). Negative
controls (each restored afterwards): printing the raw path from
`goio::path_error_text` fails 43 of the first 79 cases; dropping the
`read segment <author>:` wrapper fails 14 of 143; ignoring `$PWD` in
`goio::getwd` fails 2; letting the first duplicate `id` win in `room_config`
fails 3; requiring an `id` (the former `toml::from_str`) fails 22.

ROOMCLI-001 negative controls on `goflag.rs` (each restored afterwards): an
undefined flag keeping `=value` in its name fails 112 of 2231 cases; treating a
lone `-` as a flag fails 134; not consuming the `--` terminator fails 40;
accepting misplaced int underscores (`1__0`, `1_`) fails 2.

## Slice 3: identity provider chain (ROOM-001)

`testdata/port/room/identity-provider-cli.json` pins `identity.Load` through
the real Go process (`identity show`/`export --public`) in a disposable
HOME/XDG root per case with fake `symvault`/`security` executables (Go-built
helper for the oracle, `rustc`-built helper for the replay; both log argv and
print configured stdout/stderr/exit). Each case records exit code, both
streams, the provider call trace and the full on-disk tree (a stray write by
the command would show up).

Probe result before the fix: 16 of the first 70 cases differed.

| Class | Go | Rust before |
|---|---|---|
| 64-byte key with a foreign public half (env or `symvault`) | accepted; member id and public key come from the stored half | rejected, fell through to the next chain |
| identity file with duplicate keys | last wins | `duplicate field` error |
| identity file keys differing in case, Kelvin sign | folded match | ignored the key (`invalid key data`) |
| identity file `null` values / array, number, string root | no-op / `json: cannot unmarshal ... into Go value of type identity.StoredIdentity` | serde type error or accepted |
| syntax and type error text | Go wording (`unexpected end of JSON input`, `invalid character 'x' ...`, `json: cannot unmarshal number into Go struct field StoredIdentity.private_key of type string`) | serde wording |

Negative controls on the Rust sources (each restored afterwards, run against the
final 104-case fixture unless noted): restoring the seed/public-half check
fails 3 (of 97), ignoring the `symvault` exit status 1, accepting a 32-byte
provider seed 2, not trimming provider output 1, a different `symvault` key
path 27, not trimming the env key 1, file-before-symvault ordering 17,
exact-case-only keys 1, first-duplicate-wins 1, dropping the root type error 4,
raw (unescaped) high bytes in error quoting 3, `String::from_utf8_lossy`
instead of Go's per-byte U+FFFD 1, a lone surrogate not becoming U+FFFD 1,
dropping the Kelvin fold 1, folding long s to K 1. An "accept any env key
length" mutant survives because `identity_from_private_key` independently
rejects other lengths (equivalent mutant, not counted).

Windows: the provider helper is compiled with `rustc` (Rust) / `go build`
(Go oracle) and installed as `symvault{EXE_SUFFIX}`; `PATH` is the scratch
tools directory only; `HOME`, `USERPROFILE`, `XDG_DATA_HOME`, `TMP`, `TEMP`
and `TMPDIR` point into the scratch root; tree names are `/`-normalised.
Residuals: native Windows/Linux receipts; identity files containing invalid
UTF-8 are covered at the unit level (`gojson` tests) and for syntax errors, not
for field values (the fixture is JSON and cannot carry them except via
`content_hex`, which the cases use only for error positions).

## Slice 4: process fixtures for the remaining functional residuals

Real Go processes, recorded by `cmd/symroom/port_mcp_process_contract_test.go`
(`testdata/port/room/mcp-process.json`, 15 cases) and
`cmd/symroom/port_store_cli_contract_test.go`
(`testdata/port/room/store-cli.json`, 131 cases); replayed by
`crates/symroom-cli/tests/mcp_process.rs` and `store_commands.rs`.

- **MCP-004** concurrency: Rust queues calls beyond 512 workers; accepted,
  pinned difference (see the matrix row).
- **MCP-005** cancellation and shutdown: EOF, `notifications/cancelled`,
  truncated/invalid/oversized frames, failed writes (`EBADF`), broken pipe
  (SIGPIPE), SIGTERM/SIGINT.
- **ROOMCLI-002** identity store and `index rebuild` success.
- **ROOMCLI-001** lossless non-UTF-8 argv (`goio::os_text` / `text_os`).
- **ROOM-001** Go-identical 64-byte-key signing (`identity::sign_with_stored_half`).

Negative controls, each applied to the Rust source, run against the committed
fixtures and restored afterwards (the tree was clean afterwards): cap raised to
100000 fails `six-hundred-concurrent-waits-answer-ping-first`; SIGPIPE left
ignored fails `broken-pipe-ends-with-sigpipe`; non-UTF-8 argv bytes turned into
U+FFFD, a zeroed public half in the signature hash, and a changed `index
rebuild` success line each fail the store replay.

Local receipts at `5dd326453689dd347c9923fea15df4e59f710822` (macOS arm64
`macos-5dd32645-144706.log`, Linux arm64 `linux-5dd32645-151118.log`): all gate
targets exit 0. The Linux replay counts 3 macOS-only file-name-policy cases as
SKIPs (131 total, 128 replayed); macOS replays 131 of 131 and 15 of 15 MCP
cases. `cargo +nightly-2026-09-03 fuzz run room_event` ran 1,753,724 cases in
121 seconds, exit 0.
