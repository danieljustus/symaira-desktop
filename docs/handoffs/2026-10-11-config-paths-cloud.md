# Config-paths continuation checkpoint

## Goal and boundaries

Resume the bounded `symdesk config paths` parity correction in PR [#1245](https://github.com/danieljustus/symaira-desktop/pull/1245), addressing [#1244](https://github.com/danieljustus/symaira-desktop/issues/1244) and native test failures in [#1246](https://github.com/danieljustus/symaira-desktop/issues/1246). This is an unfinished source checkpoint, not merge, release, cutover or full migration acceptance. [#1135](https://github.com/danieljustus/symaira-desktop/issues/1135) remains open.

The local execution was explicitly stopped for this handoff. Do not restart historical observers or automated backlog work. Start only the concrete continuation requested by the next operator.

### Completed code

- The staged Rust command retains Go output/error ordering, config/environment/flag precedence, legacy migration and eight resolved path fields. The reviewed implementation and its previous corrective commits are reachable from the base code revision below.
- The source-bound native corpus declares 75 cases. Unix must execute 75 Go/Rust pairs; Windows must execute 59 and explicitly classify 16 Unix-only cases as not applicable.
- Hosted oracle builds use an independent Git clone with a real `.git` directory at the pinned foundation. Cache discovery retains operator Windows `LOCALAPPDATA`, while execution replaces it with a private runtime path.
- This checkpoint additionally preserves the independently reviewed Windows test-fixture correction: track temporary `source.txt -text` attributes and write the fake child's stdout/stderr as explicit bytes. Byte/hash/exit/timeout and oracle assertions are unchanged; no production capture normalization was introduced.

### Next bounded task

Diagnose the Linux ARM failure in `ai_secrets::tests::process_runner_discards_stderr` using fake-only isolated commands. Distinguish spawn failure, shell nonzero status, reader failure and deadline/EOF behavior before changing source. The original CI shows `None` instead of the fake stdout, not a proven cause. Do not classify it as a flake, add retries, increase the production timeout, suppress its assertion or invoke a real credential provider without causal evidence.

After that diagnosis, finish one coherent correction, regenerate genuine generator provenance, validate the resulting private fixture commit, then execute current-head gates. No merge until all contractual and required checks pass on the actual final code head and the changed scope has independent review.

## Source identity

| Repository | Remote branch | Base code commit | Role |
|---|---|---|---|
| https://github.com/danieljustus/symaira-desktop | `fix/cfg1244-native-gate-final` | `dfcc1e95c74f5fd4702dcdcd5c94ff846e7a1cd8` | Previous published and locally accepted candidate, with known remote failures |
| Same repository | `main` | `47dd417ec5632c8e6a2edb32ead09db09e929ed9` | Integrated foundation and native Go command oracle |

The immutable final commit containing this handoff is identified in the external verification comment and continuation request, not recursively in this document. A later branch tip must not silently replace that verified commit.

The corrected `scripts/rust-port/test_config_paths_native.py` has SHA-256 `a14b3e5070758eda3523b06ae9ccde9a4926368269c719a8bc4c71429e5fdd5d`. Its delta from the base code is four added and two removed lines.

### Interrupted diagnostic artifact

The remaining read-only diagnosis was interrupted on explicit closeout. Its fake-only probe and exact original result are preserved in `docs/handoffs/2026-10-11-config-paths/process_probe.rs` and `process-probe-linux-arm64.txt` in that directory. The published probe differs only by a compile-time `concat!` spelling of the identical missing-executable path, avoiding a provider-format-like literal in source. The original source is retained locally; its SHA-256 is `644aa120b58cb08bbdf3037f495dfede714ec41a933fda834153629c38c7b81f`. The original sandboxed Linux/ARM run exited 0 with Rust `1.98.1`, demonstrating four explicit controls: ordinary fake stdout with discarded stderr, nonzero shell status, missing executable and a descendant holding stdout open. It directly invokes `/bin/sh`; it does **not** reproduce the freshly written executable-script fixture that failed CI. No actual CI failure cause or production correction was established. This is reusable diagnostic WIP, not pinned-toolchain acceptance.

The diagnostic worker's assigned checkout was the integrated foundation rather than the requested candidate. The parent verified that `crates/symdesk-cli/src/ai_secrets.rs` is byte-identical at both revisions, SHA-256 `d418634768a6e1ffd5fd4e66328f96181cd3249a946cab78054d11a09dfcd30f`. This preserves the probe's bounded applicability, not a broader candidate review. No worker process or container remains running.

## Required inputs and disposition

| Input | Location / identity | Disposition |
|---|---|---|
| Implementation, Make targets, CI, corpus and regression controls | Tracked files in this branch; `scripts/rust-port/config_paths_native.py`, `scripts/rust-port/cmd/config-paths-diff`, `crates/symdesk-cli` | Published with this checkpoint |
| Canonical fixture source P | `testdata/port/provenance.json`: `d0092bb10282c16ef43f70b20c2a00ffd43ffec6`, release `post-v0.12.2-security-880` | Reproducible from its tracked native Git bundle, even after squash |
| Source bundle | Same JSON, anchor `bbfa4cbdaefcb36784c8cdc1051c19ebca0bb7cc`, SHA-256 `11eedfc10dcea2743672c344a0e134919fe8a06b8efbb1cc88fbb9f10013976e` | Published and digest verified; never hand-edit or relabel |
| Go dependencies | `go.mod` / `go.sum`, Go `1.26.9`, CoreKit `v0.18.2` | Rebuild through normal Go package resolution |
| Rust dependencies | `Cargo.toml`, `Cargo.lock`, `rust-toolchain.toml`, Rust `1.98.0` | Rebuild through Cargo's locked resolution |
| Partial fake-only diagnostic | `docs/handoffs/2026-10-11-config-paths/process_probe.rs`, SHA-256 `f3f7ea0a8c1c9701464a5deb01f3afc5fa3c955ee739f52572d8933a0d73729d` | Published with only the equivalent compile-time literal split described above |
| Original bounded Linux ARM probe output | `docs/handoffs/2026-10-11-config-paths/process-probe-linux-arm64.txt`, SHA-256 `1c597bfe924636febb7e4c62dbb96e9e14735fe50b8687ef493ab91b9a7a7430` | Exact original bytes copied and published; Rust 1.98.1, not actual CI reproduction |

CoreKit Git pins in the workspace are `d382b8615ce879bac6f23c7250e13667934e8f93`, `0f441fb27872da7edd65244e4fcbd0890f0b7174` and `04d1411adb57aa602b992509121011aa7666ff1a`. They must remain fetchable from `https://github.com/danieljustus/symaira-corekit`. A target-runtime fetch failure is a real setup blocker, not permission to alter a pin.

There are no tracked `.gitmodules` or `.lfsconfig` files. No submodule/LFS input is required for the focused continuation. Superseded review/diagnostic worktrees and merged historical candidates are not needed to reconstruct the current implementation; they were retained locally without deleting unique history. No stash supplies required source.

Build outputs, module/toolchain caches and temporary product/test stores are regenerable, not authoritative inputs. Historical local verification wrappers, raw chats, private run checkpoints, operator settings, credentials, personal documents and unrelated worktrees are not needed by the commands below and are deliberately not published. Do not depend on a local absolute path, a prebuilt product binary or an installed agent skill.

## Known source-provenance debt

The Windows correction changes a fingerprinted generator input. The existing `generator_source_digest` still describes the preceding candidate, so fixture/provenance checks are not accepted for this checkpoint. This is deliberate WIP preservation, not a provenance exemption.

Use `docs/rust-port/fixture-identity.md` and the actual `portgen` CLI. Keep the canonical Go production oracle and observed fixture bytes unchanged unless a separately reviewed production change requires otherwise. Generate the real patch from a clean committed checkout, review every changed output, run `portgen --apply-artifact`, and prove the adopted tree equals the validated tree. Do not directly replace a digest or weaken dirty-source, ignored-input, immutable-source or mutation guards.

The fixture-only refresh command, after all source corrections are committed and the working tree is clean, is:

```sh
make port-fixtures-generate > "$CHECKPOINT_OUTPUT/generated.patch"
go run ./scripts/rust-port/cmd/portgen --apply-artifact "$CHECKPOINT_OUTPUT/generated.patch"
```

`CHECKPOINT_OUTPUT` must be an existing private output directory outside the checkout. Generation and validation are pending, not executed for this checkpoint.

## Reproduce from GitHub

Use a full, independent clone from the public remote, with no local source clone, alternates, copied settings or product binaries. Checkout the immutable commit from the external verification receipt. Working directory for all commands is the repository root. Read `AGENTS.md` and the current tracked migration documents before editing.

Required focused setup: Git, Python 3, Go `1.26.9`, Rust/Cargo `1.98.0`, a POSIX shell and Make. Native Windows workflow uses Git Bash. Follow the checked-in toolchain and lockfiles, not whatever compiler happens to be installed.

Before the commands, export `CHECKPOINT_OUTPUT` to an existing private directory outside the checkout, following the host's storage policy. This directory supplies only disposable test/materialization outputs, never copied source or product state. On the original guarded Mac, use the approved external build area for both the remote-only verification checkout and its outputs. `GOTOOLCHAIN` below selects the exact Go compiler; no undocumented source or runtime override is required.

```sh
export GOTOOLCHAIN=go1.26.9
: "${CHECKPOINT_OUTPUT:?Set an existing private outside-checkout output directory}"
python3 --version
git --version
go version
rustc --version
go mod download
python3 scripts/rust-port/test_config_paths_native.py -v
go test -count=1 ./scripts/rust-port/inventory -run '^TestGeneratorDigest'
go run ./scripts/rust-port/cmd/portgen --oracle-source-dir "$CHECKPOINT_OUTPUT/oracle"
```

The materialized source must have HEAD `d0092bb10282c16ef43f70b20c2a00ffd43ffec6`. The final command verifies and materializes the bundled source; it does not generate fixtures or grant native acceptance. The destination must not already exist.

To exercise the retained analogue without credentials or Cargo dependencies:

```sh
rustc --edition=2024 docs/handoffs/2026-10-11-config-paths/process_probe.rs -o "$CHECKPOINT_OUTPUT/process-probe"
"$CHECKPOINT_OUTPUT/process-probe"
```

These four diagnostic controls are not the actual failed unit test and cannot substitute for its Linux ARM reproduction.

For a Unix-only focused Rust diagnosis, once its exact source is understood:

```sh
cargo test -p symdesk-cli --bin symdesk --locked ai_secrets::tests::process_runner_discards_stderr -- --exact --nocapture
```

Do not call `resolve_key` with real user configuration. Tests must use fake credential tools or fake shell scripts only. No model/API key, vault credential, GUI, Keychain or production service is required for the focused checks. Linux ARM is required to establish that target's failure/recovery; macOS success alone does not do so.

The existing `make config-paths-native CONFIG_PATHS_EVIDENCE_DIR=<fresh-outside-checkout>` builds and captures actual native binaries. Four Unix targets and both Windows architectures must be genuinely exercised before promotion. CI dispatch is not automatic handoff behavior and is not started by this document.

On the original macOS machine, external-storage/build guards remain mandatory and prohibit internal fallback; invoke its existing build launcher rather than changing storage policy. The unresolved local volume-root permission decision blocks broad local filesystem/SymRoom acceptance only. It is not authorization to change permissions and is not an input requirement on a different, correctly provisioned cloud filesystem.

## Evidence and unresolved gates

| Check | Source/input | Actual result | Reachable record |
|---|---|---|---|
| Full Go race and coverage | Base `dfcc1e95…`, Go `1.26.9` | 132 packages, 2,878 named top-level passes, 70.2% coverage; unchanged 70% floor and strict lint passed | [Published verification](https://github.com/danieljustus/symaira-desktop/issues/1246#issuecomment-6107041384) |
| Native config captures | Base `dfcc1e95…`, Linux x64/ARM and macOS ARM/Intel | Four original captures each passed 75 pairs; all 1,200 case streams and 112 command streams independently rehashed | [Failure and capture record](https://github.com/danieljustus/symaira-desktop/issues/1246#issuecomment-6107227072) |
| Python fixture correction | File hash above, Python `3.14.2` on macOS | 10 tests passed normally and 10 with private inherited `core.autocrlf=true`; specification and quality/security delta reviews passed | Independent review recorded in the final publication comment; no Windows recovery inferred |
| Original Windows controls | Base `dfcc1e95…`, both architectures | Exactly two CRLF fixture failures before native capture | [Run 38124077632](https://github.com/danieljustus/symaira-desktop/actions/runs/38124077632) |
| Linux ARM workspace | Base `dfcc1e95…` | Existing fake-process stderr test failed: `None` versus expected fake stdout; cause unresolved | Same run, job `114424664115`, `ai_secrets.rs:224` |

The original run was still in progress at closeout inspection, with confirmed failures; it was not manually cancelled. Its raw CI artifacts expire on 2026-10-25 and are optional historical diagnosis aids, not durable required inputs or successor approval. The repository can rebuild all captures with the tracked targets. The next engineer must obtain fresh original evidence for the changed head rather than relying on expiring artifacts.

Fresh GitHub-only checkout at final handoff HEAD: **pending until the external verification comment is published**. The final comment records exact commands, outputs, immutable head, remote/handoff readback and any setup blocker. Never interpret earlier-head success as final-head success.

Target cloud runtime: **not checked**. Network access to GitHub, Go proxy/checksum service and Rust registries/toolchain distribution must be separately verified there. Signing, notarization, iOS device acceptance, macOS GUI and full migration gates are outside this focused checkpoint and not established by generic cloud execution.

## Automation and safe continuation

PR #1245 is intentionally Draft and auto-merge is off. The local CI observer ended at its 900-second bound. No new CI observer, repair wave, cloud job, paid model, merge, release or destructive cleanup was authorized by the closeout. The remaining read-only Linux diagnosis ended interrupted; its exact partial probe/log are retained above, and no verified CI cause was returned. No local worker or terminal process remains active. Late completion messages are historical results, not automatic restart requests.

Preserve strict provenance, raw byte comparison, 75/59/16 applicability, the unchanged coverage threshold and repository protections. Do not close #1244, #1246 or #1135 based on this WIP snapshot. Verify the immutable head and setup, diagnose the bounded Linux ARM failure, then follow the explicit gates above.
