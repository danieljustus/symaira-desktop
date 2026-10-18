# Config-paths fixture provenance continuation

Repository: `danieljustus/symaira-desktop`. Remote branch: `handoff/20261012-config-provenance`.

Status 2026-10-18: this checkpoint was closed unmerged as [PR #1252](https://github.com/danieljustus/symaira-desktop/pull/1252) and its work was integrated through [PR #1245](https://github.com/danieljustus/symaira-desktop/pull/1245). The branch is deleted; its head remains fetchable as `refs/pull/1252/head`. The text below describes the state at the time.
Base code commit: `996e7a7a739b9fa0ae4091fb1c11c6dd1fb6c2b2`. This is a separate unfinished alternative to the PDF/MCP continuation; do not merge them implicitly. Existing [PR #1245](https://github.com/danieljustus/symaira-desktop/pull/1245) remains unchanged.

## Preserved delta

Four local commits after `1a76135a23d6f1718a8c707b83e8a6522648e9ea` change exactly `crates/symdesk-cli/src/ai_secrets.rs`, `scripts/rust-port/cmd/portgen/fixture_checks.go`, `scripts/rust-port/cmd/portgen/provenance_commit_test.go` and `testdata/port/provenance.json`. The fake-process fixtures use the existing shell rather than newly created transient executables; the producer environment retains only the intended GOMAXPROCS control, and the regression asserts both the environment value and effective runtime limit. Generated provenance was committed locally. This is preservation, not a new native acceptance claim.

Read the retained `2026-10-11-config-paths-cloud.md` for the complete source/bundle, toolchain and 75-Unix/59-Windows/16-exclusion contract. Its former next-action and pending-generation statements describe the earlier `1a76135a` checkpoint; this later checkpoint preserves the corrections without declaring full completion.

## Next task and reproduction

First execute the existing noncompiling fixture tests in a direct checkout at the final HEAD:

```sh
python3 -B scripts/rust-port/test_config_paths_native.py -v
```

This exact final-head execution is pending until the external PR verification comment is available. Then independently reconcile the current generated provenance and source/compiler input closure, actual adopted-tree equality and remaining native matrix on this candidate. Use the repository's portgen tools, never hand-edit a digest or reuse an earlier-head pass as acceptance. The canonical Go source `d0092bb10282c16ef43f70b20c2a00ffd43ffec6` and its tracked bundle remain unchanged. Go 1.26.9 and Rust 1.98.0 are required for their compiler gates.

Historical host wrappers and raw verification logs remain local, not portable acceptance input supplied by this document. Reproduce current gates using the tracked commands or obtain the exact retained evidence through an approved access route before claiming reuse. Native Linux ARM failure causality must remain distinct from the controlled test correction; macOS success does not prove all targets. No full current-head native matrix is accepted here. Target cloud runtime and access are not checked.

## Shared boundaries

This is preserved work in progress, not migration completion or merge/release approval. Preserve exact Go/Rust byte contracts, original failed captures, source/compiler provenance, security policies and independent native-platform gates. Cross-compilation and source-only tests do not prove native execution. Never manufacture expected results, weaken a deadline, discard a mismatching row or change an oracle only to make a comparison pass. Signing, releases, productive cutover, Go retirement and destructive cleanup require separate authorization. Do not start paid models or services.

Local automatic continuation has been disabled and the owned unfinished work parked. This document does not restart automation. Existing remote CI may run from the ordinary push; no workflow dispatch, merge, tag or release is requested.

## Reproduction environment

Start at the repository root of a direct GitHub clone and check out the immutable final HEAD provided by the external verification receipt. Do not substitute the branch's later tip. Use Python 3 (closeout host: 3.14.2), Git (closeout host: 2.54.0) and the exact Rust 1.98.0 components in `rust-toolchain.toml`. `Cargo.toml`/`Cargo.lock` and, for Go oracles, `go.mod`/`go.sum` remain authoritative. GitHub and the public package registries must be reachable. No package installation or cloud runtime is implied by these instructions.

For local Mac development, respect the host's existing storage/build launcher and reserve policy. The host's mounted-drive paths and private launcher are not cloud inputs: use ordinary owned output/temp/cache directories on a suitable cloud filesystem instead, without weakening product permission checks. Use isolated HOME/XDG fixture state, never real user stores. Native Apple GUI, Keychain/Touch ID, signing and filesystem tests require the real supported OS/hardware and authorization. Native Linux and Windows observations must run on those systems.

Fresh-checkout verification at the final handoff HEAD is pending until the separate PR comment is published. The external comment records exact commands, exits, failures and runtime limits without recursively changing this commit. A successful source inventory or Python test is not a full application build.

## Retained and excluded inputs

Build outputs, package caches, Python bytecode, local control databases, private orchestration reports and the completed local cache-cleanup script are not shipped. None is an allowed replacement for missing source. Originals remain local. Historical local acceptance receipts are not silently treated as public or independently verified by this document; any needed unpublished immutable receipt remains an explicit blocker below. No credentials, `.env` values, production data, personal memory or signing keys are included. Provision target credentials only through the operator's approved secret manager if a later explicitly authorized operation needs them.

No submodules or Git LFS inputs were found in the inspected source inventory. Check locks and any downstream platform-specific dependencies again before their own gates. The portable source checkpoint is separate from native-runtime, access and acceptance readiness.
