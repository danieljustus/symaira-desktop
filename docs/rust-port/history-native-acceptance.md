# Native history core acceptance

Accepted runtime source: `ca08831a8c60ae2b6a268741e2f7da004dab3f7c`.
Branch: `migration/consumers-slice-history-accept`.
Native run: https://github.com/danieljustus/symaira-desktop/actions/runs/34945251718

This accepts only the existing Snapshot/List/Content/Restore core and its history
oracle/replay harness. It does not complete VAULT-006 or RUST-007. Checkpoints,
undo integration, trash/purge, retention, remaining vault writes and product
CLI/API integration remain outside this slice. The full-candidate VALUE gate,
including the unchanged 20% size-or-median-RSS improvement and <=10% p95 ceiling,
is not remeasured or declared complete here. Go remains production and rollback.

## Executed native evidence

The `Run native history differential` step succeeded on all three platforms.
Each raw report records Go 1.26.6 and Rust 1.98.0, four command exit codes of zero,
56 contiguous Go operation IDs (1..56), seven executed Rust history tests with
zero ignored/filtered tests, and identical before/after source inventories.
The replay includes snapshot deduplication, binary/Unicode paths, restore,
missing/null/duplicate/case-folded manifest fields, timestamp offsets, malformed
manifests, invalid IDs, traversal rejection and final filesystem comparison.
Unix permission assertions remain Unix-specific; this is not Windows ACL proof.

| Native target | Raw Go oracle SHA-256 | Raw report SHA-256 |
|---|---|---|
| Darwin arm64 | `d8cbf7521a15fe66ccd234baec48b7d519fef4c85d30fde03967466394940b5b` | `1f6483eff61a438911d9b197325f49b65b9836620891b32954fd7524fd6b3495` |
| Linux x86_64 | `515af00d08527e91424d6b27fc2f3ebe2281b85cc830b4e9b1eb939ddb467396` | `2a7d573625232304cb6945b5fbb889dc523c73c839561a68172c3d3cd79642b2` |
| Windows AMD64 | `772ec5f2d03917455a5124daaf93bb5b9db5a0d9eaadabc55268b1c85c2e1ff0` | `733cbb1a5327cf41c4010441a7aec4becdd58d9d2d73fbabc6e09b1b505b4caa` |

Artifact names are `history-{macos,ubuntu,windows}-latest-<accepted-source>`.
Raw artifacts, job logs, original failing runs and local command records are
retained in the task evidence archive, not replaced by this summary. Source
manifest comparisons retain raw native hashes; LF/CRLF-equivalent Git text is
recognized only when comparing native checkouts to committed source bytes.
Oracle/report digests above are exact bytes, without normalization.

The genuine Go oracle is pinned to
`c4f6e77928849c3626400c45676c05c3cf58f2a1` (`unreleased-c4f6e779`). Its production
source hash inventory was independently compared with `git show` bytes. No Go
production or Rust history implementation changed in this acceptance branch
relative to the supplied `24b789d5da48d454ec92971996ea5fd6cd83aee3` base.

The historical acceptance pin and hashes above remain scoped to that run.
The later integration branch re-pins the current oracle to
`6a91639f4f6ef8201cf4cbe7eed6ccc77a3874f1` after the Go dependency
refresh; it must obtain its own native evidence rather than inherit these hashes.

## Native defects repaired without weakening guards

1. `34939911801`: Windows had no Go cache location in the isolated environment.
   Allocate private GOCACHE alongside HOME/XDG/temp, never ambient AppData.
2. `34940679653`: the Unix HTML filename contained Windows-forbidden angle
   brackets. Preserve the original Unix filename; use
   `html/ampersand&injection.md` on Windows, retaining HTML-sensitive JSON
   escaping and all 56 operations. This is an explicit platform-specific input,
   not a claim that Windows accepts angle-bracket filenames.
3. `34941418277`: stripped MSVC installation roots caused Git's Unix `link.exe`
   to be selected. Preserve only ProgramFiles/ProgramFiles(x86), with
   case-insensitive environment-key matching. Ambient AppData and unrelated
   credential-path variables remain excluded.
4. `34942550368`: both implementations refused `/abs/evil.md`, but Go's native
   `os.Root` error was classified as `other`. Classify a typed `os.PathError`
   with exact inner `path escapes from parent` as `invalid_path`; preserve the
   raw error and original rooted-path operation. Path text containing that
   phrase and unrelated permission failures remain `other`. No resolver change.
5. `34943437227`: all native Rust replays passed, but the new production-Go
   rooted-path unit retained Store's cached root handle through Windows TempDir
   cleanup. Execute that assertion in a bounded child, verify its named PASS,
   and wait for process exit before parent cleanup. No sleep, forced GC, skipped
   test, ignored cleanup failure or production API change.

## Local and negative controls

For each repair, the owning worker executed the existing `make
history-differential` under private runtime directories and the shared consumer
heavy-slot wrapper. The final generator edits also passed Go generator tests,
Go 1.26.6 gofmt, genuine `portgen` regeneration and `portgen --check`. Only the
tracked generator-source digest changed; production provenance remained fixed.

The runner's failure/timeout/compiler/execution/source-drift controls pass.
Removing private GOCACHE from an actual copied runner made its regression fail
(exit 1); the unchanged runner then passed the real Go/Rust differential.
The pre-existing five source mutants remain historical evidence under the
run76 source hashes, not newly executed mutations of this branch. Production
history bytes still match those historical restored bytes; generator/test-only
changes are explicitly newer. Native strict path/provenance controls executed
again in the accepted run.

## Integration contract

The final handoff commit is a descendant of the accepted runtime source, not a
new native measurement. Run 34945251718 passed all three native history jobs and
all three Go port-contract jobs, but the aggregate CI result was 14/15 because
gosec flagged the test's `os.Executable` subprocess as G204. The final delta adds
only a call-site justification (this test binary, fixed arguments), regenerates
the generator provenance through portgen, and records this documentation.
Exact golangci-lint 2.12.2, generator tests, provenance check, runner controls,
the real cache-removal mutant and local history differential pass after that
delta. No executable behavior or security policy was relaxed.

Under the operator's final steering, Windows acceptance of the final integrated
consumer is deferred to the integrator; the genuine all-three-OS evidence above
remains scoped to its exact captured source. No new Windows-only cleanup loop is
required to hand off this bounded milestone. The integrator must merge this
branch with an ancestry-preserving merge commit, retain the independent
frontmatter slice, and run history/native gates on the resulting exact HEAD.
Do not squash, reset the integration worktree, alter the pinned oracle, delete
Go, publish a release, or promote RUST-007/RUST-008 from this narrow acceptance.
