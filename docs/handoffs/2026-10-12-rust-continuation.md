# Rust PDF and MCP continuation

## Goal and source identity

Repository: `danieljustus/symaira-desktop`. Remote branch: `handoff/20261012-rust-wip`.
Base code commit: `db1a447bcda9c9c1f0f58e8c052b8afdbfccfca8`, preserving thirteen renderer/Go-oracle commits and eight WIP files listed in `2026-10-12-rust-inputs.json`. The separate config-path PR [#1245](https://github.com/danieljustus/symaira-desktop/pull/1245) is not integrated by this checkpoint.

Preserved work covers bounded PDF tool execution, validated output publication, process-group cleanup, versioned source-bound Go/Rust PDF fixtures, profile discovery, and unfinished native PDF MCP consumers. See `docs/rust-port/pdfops-v2.md` for the current contract; earlier v1 failures in `pdfops-local.md` remain historical evidence, not the current safety policy.

Go reference `28cfedc2d062c4656d0a79e506ae16d5a61ef13d` and the intermediate fixture-producing commits remain in this branch's ancestry. Keep that ancestry reachable when eventually integrating under squash-only policies. Public CoreKit pins include `0f441fb27872da7edd65244e4fcbd0890f0b7174`, `d382b8615ce879bac6f23c7250e13667934e8f93` and `04d1411adb57aa602b992509121011aa7666ff1a`.

## Concrete next task

The lockfile/compiler repair is now implemented: `Cargo.lock` records the existing `symdesk-render` dependency, the preserved MCP files are rustfmt-clean, and the pending-call tuple has a type alias for strict Clippy. No dependency version or runtime behavior was changed. The historical input inventory remains a record of the original preserved bytes, not hashes of this repaired candidate. Do not edit the original frozen Go reference or rewrite prior captures. Run:

```sh
python3 -B crates/symdesk-cli/tests/mcp_pdf_differential.py --help
cargo metadata --manifest-path "$PWD/Cargo.toml" --locked --format-version 1 --no-deps
cargo fmt --manifest-path "$PWD/Cargo.toml" --all --check
cargo check --manifest-path "$PWD/Cargo.toml" --workspace --all-targets --all-features --locked
cargo clippy --manifest-path "$PWD/Cargo.toml" --workspace --all-targets --all-features --locked -- -D warnings
cargo test --manifest-path "$PWD/Cargo.toml" -p symdesk-cli --bin symdesk --locked mcp:: -- --nocapture
GOTOOLCHAIN=go1.26.9 CGO_ENABLED=0 GOFLAGS='-mod=readonly -p=2' go test -count=1 -v ./scripts/rust-port/pdfops/reference ./scripts/rust-port/fixtureoracle
```

These commands passed on the repaired local macOS arm64 source snapshot with Rust 1.98.0 and Go 1.26.9. The focused MCP run executed 19 passing tests. The source controls executed one PDF-reference test and five top-level fixture-oracle tests, including rejection of historical production and mismatching source identities. Workspace compiler and strict Clippy checks passed; neither is a full workspace test or native-matrix acceptance. The argument-help command only validates loading and parsing. The final independent GitHub-checkout receipt must bind these results to the published candidate separately.

The next unresolved task is native acceptance at the repaired candidate. CI run [38225830585](https://github.com/danieljustus/symaira-desktop/actions/runs/38225830585) found the same strict-Clippy `unused_mut` error on both Windows architectures at code commit `5eeebdcc8e5843088db8338c65571fd986376548`. The mutable `DirBuilder` is now confined to the Unix branch; its 0700 mode remains unchanged. Local host and Windows-target strict Clippy pass. The local workspace compiler/Clippy gates, 119 CLI/renderer tests and 13 native workflow controls pass. Compilation is not native execution.

The manual CI suite now has a separate `PDF MCP native` job on Linux and macOS. It verifies the unchanged Go source controls, builds both production MCP executables at the checked-out revision using Go 1.26.9 and Rust 1.98.0, and executes `crates/symdesk-cli/tests/mcp_pdf_differential.py` with installed `pdfinfo`, `qpdf` and `pdftotext`. Runtime roots are fresh owned children of runner HOME, not an insecure shared temporary ancestor. No product permission check is relaxed. Reports retain binary/tool hashes, exact case IDs, raw streams, input-preservation and cleanup observations. Native execution is pending until the separate exact-head receipt verifies it; a prepared job is not a passing probe.

The Go lint configuration documents narrowly scoped rule/path/source exceptions for caller-selected local-file copies and disposable test fixtures. It does not disable gosec or alter pinned Go production/generator bytes. The arbitrary local-file API is not a vault-confinement boundary. Do not extend these exceptions to unrelated file access or command execution.

The existing renderer gate is:

```sh
make render-differential RENDER_EVIDENCE_ROOT=<new-owned-evidence-directory> RENDER_RUNTIME_ROOT=<new-owned-runtime-directory>
```

Use the optional `RENDER_BUILD_LAUNCHER` only where the local host actually requires its existing guard. This is a future full-gate command, not a closeout success claim. See the two PDF contract documents for scope and tool versions.

## Unresolved inputs and acceptance gates

- The preserved PDF/MCP wiring now compiles and passes focused unit tests. Complete paired MCP acceptance, full renderer replay, unreached symroom suites, native target matrix and RUST-011 remain open.
- The prior Darwin index fixture failed the actual full-ancestor trust check on a group-writable/noowners external volume. A private child directory alone does not repair this. Use a genuinely trusted native runtime with its full ancestor/owner/ACL chain; do not relax product checks or call Linux Darwin proof.
- Host-specific secure-image provisioning/launcher proposals and raw prior acceptance logs remain local, uninstalled and not accepted. They are not required for a portable cloud code build, but are required before reusing that particular Mac setup or claiming its old raw evidence has been independently revalidated. No public evidence URL is asserted for them.
- Local storage reserve was restored; that is not runtime, compiler or native acceptance. No new production service is authorized.
- GitHub repository visibility, identity, ADMIN access and enabled Actions were checked. Standard public runners for the existing six native labels are documented as free. The account Actions budget was read from GitHub's authenticated billing page: zero dollars with stop-usage enabled. The existing manual CI suite also uploads artifacts and uses caches; those storage charges are separate from free runner compute and must remain covered by that effective spending stop. Complete native acceptance is still pending. The dedicated manual PDF/MCP jobs retain observations for 14 days, not indefinitely; their scripts and required inputs remain versioned for fresh reproduction. Retrieve and validate reports before expiry. No signing or service credentials are required by this paired probe.

## Shared boundaries

This is preserved work in progress, not migration completion or merge/release approval. Preserve exact Go/Rust byte contracts, original failed captures, source/compiler provenance, security policies and independent native-platform gates. Cross-compilation and source-only tests do not prove native execution. Never manufacture expected results, weaken a deadline, discard a mismatching row or change an oracle only to make a comparison pass. Signing, releases, productive cutover, Go retirement and destructive cleanup require separate authorization. Do not start paid models or services.

Local automatic continuation remains disabled and the older unfinished work remains parked. This document does not restart that automation. A separately requested bounded native CI verification may run at the published candidate under the verified zero-spend stop; its exact run and outcome belong in the external receipt. No merge, tag, release or productive cutover is authorized by this handoff.

## Reproduction environment

Start at the repository root of a direct GitHub clone and check out the immutable final HEAD provided by the external verification receipt. Do not substitute the branch's later tip. Use Python 3 (closeout host: 3.14.2), Git (closeout host: 2.54.0) and the exact Rust 1.98.0 components in `rust-toolchain.toml`. `Cargo.toml`/`Cargo.lock` and, for Go oracles, `go.mod`/`go.sum` remain authoritative. GitHub and the public package registries must be reachable. No package installation or cloud runtime is implied by these instructions.

For local Mac development, respect the host's existing storage/build launcher and reserve policy. The host's mounted-drive paths and private launcher are not cloud inputs: use ordinary owned output/temp/cache directories on a suitable cloud filesystem instead, without weakening product permission checks. Use isolated HOME/XDG fixture state, never real user stores. Native Apple GUI, Keychain/Touch ID, signing and filesystem tests require the real supported OS/hardware and authorization. Native Linux and Windows observations must run on those systems.

Fresh-checkout verification at the final handoff HEAD is pending until the separate PR comment is published. The external comment records exact commands, exits, failures and runtime limits without recursively changing this commit. A successful source inventory or Python test is not a full application build.

## Retained and excluded inputs

Build outputs, package caches, Python bytecode, local control databases, private orchestration reports and the completed local cache-cleanup script are not shipped. None is an allowed replacement for missing source. Originals remain local. Required production-source bundles, toolchain/lockfiles, fixture checksums and generators are tracked and sufficient inputs for fresh reproduction. Unpublished historical raw logs are optional diagnosis inputs, not a blanket cloud dependency. Historical local acceptance receipts are not silently treated as public or as acceptance of this repaired candidate. No credentials, `.env` values, production data, personal memory or signing keys are included. Provision target credentials only through the operator's approved secret manager if a later explicitly authorized operation needs them.

No submodules or Git LFS inputs were found in the inspected source inventory. Check locks and any downstream platform-specific dependencies again before their own gates. The portable source checkpoint is separate from native-runtime, access and acceptance readiness.
