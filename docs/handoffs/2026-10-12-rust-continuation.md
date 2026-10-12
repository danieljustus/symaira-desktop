# Rust PDF and MCP continuation

## Goal and source identity

Repository: `danieljustus/symaira-desktop`. Remote branch: `handoff/20261012-rust-wip`.
Base code commit: `db1a447bcda9c9c1f0f58e8c052b8afdbfccfca8`, preserving thirteen renderer/Go-oracle commits and eight WIP files listed in `2026-10-12-rust-inputs.json`. The separate config-path PR [#1245](https://github.com/danieljustus/symaira-desktop/pull/1245) is not integrated by this checkpoint.

Preserved work covers bounded PDF tool execution, validated output publication, process-group cleanup, versioned source-bound Go/Rust PDF fixtures, profile discovery, and unfinished native PDF MCP consumers. See `docs/rust-port/pdfops-v2.md` for the current contract; earlier v1 failures in `pdfops-local.md` remain historical evidence, not the current safety policy.

Go reference `28cfedc2d062c4656d0a79e506ae16d5a61ef13d` and the intermediate fixture-producing commits remain in this branch's ancestry. Keep that ancestry reachable when eventually integrating under squash-only policies. Public CoreKit pins include `0f441fb27872da7edd65244e4fcbd0890f0b7174`, `d382b8615ce879bac6f23c7250e13667934e8f93` and `04d1411adb57aa602b992509121011aa7666ff1a`.

## Concrete next task

First resolve the pending lockfile/compiler step for the preserved MCP dependency wiring in an isolated supported environment. Do not edit the original frozen Go reference or rewrite prior captures. Run:

```sh
python3 -B crates/symdesk-cli/tests/mcp_pdf_differential.py --help
cargo metadata --locked --format-version 1 --no-deps
```

The first command was executed before publication and exits 0; it only validates loading and argument parsing. Cargo lock resolution and full build remain unaccepted. After source/lock review and a real compile, the existing differential requires explicitly source/compiler-bound Go/Rust executables plus real `pdfinfo`, `qpdf` and `pdftotext`, fresh runtime and evidence directories. Its `--help` lists every required argument. It does not build or establish provenance for arbitrary supplied binaries.

The existing renderer gate is:

```sh
make render-differential RENDER_EVIDENCE_ROOT=<new-owned-evidence-directory> RENDER_RUNTIME_ROOT=<new-owned-runtime-directory>
```

Use the optional `RENDER_BUILD_LAUNCHER` only where the local host actually requires its existing guard. This is a future full-gate command, not a closeout success claim. See the two PDF contract documents for scope and tool versions.

## Unresolved inputs and acceptance gates

- Eight PDF/MCP files were source-reviewed locally but never compiled or run as complete paired MCP acceptance. Full renderer, unreached symroom suites, native target matrix and RUST-011 remain open.
- The prior Darwin index fixture failed the actual full-ancestor trust check on a group-writable/noowners external volume. A private child directory alone does not repair this. Use a genuinely trusted native runtime with its full ancestor/owner/ACL chain; do not relax product checks or call Linux Darwin proof.
- Host-specific secure-image provisioning/launcher proposals and raw prior acceptance logs remain local, uninstalled and not accepted. They are not required for a portable cloud code build, but are required before reusing that particular Mac setup or claiming its old raw evidence has been independently revalidated. No public evidence URL is asserted for them.
- Local storage reserve was restored; that is not runtime, compiler or native acceptance. No new production service is authorized.
- Target cloud runtime, real PDF tools and native permissions: not checked.

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
