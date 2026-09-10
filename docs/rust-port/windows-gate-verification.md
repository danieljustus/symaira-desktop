# Native Windows migration-gate repair

Issue: [#895](https://github.com/danieljustus/symaira-desktop/issues/895).
Base: `2544e0630e025aede87527e5afb959714a8f7606` (PR #893).
Branch/worktree: `fix/windows-rust-gates`, `.worktrees/windows-rust-gates`.

## Confirmed failures

Run [34266283970](https://github.com/danieljustus/symaira-desktop/actions/runs/34266283970) reports success, but its raw Windows logs contain:

- A failed snapshot-path assertion: `folder/literal/name.md` versus incorrectly expected `folder/name.md`.
- `TestProvenanceVerificationPasses` failed with production digest `8a8edb2404473c68ca56859251351af145e57449322d33ed08a425be5e3352ee`, expected `ac77a66c71a0c8a5428bcfece6556c73f486ec855b75e0ab4e32fafd291db877`.
- Multi-command PowerShell steps allowed later successful native commands to hide earlier failures.

## Repair and local evidence

- Converted affected steps to explicit fail-fast Bash, including the common native Rust check/test step. Added a regression that injects external exit status 23 into all five relevant step prologues and verifies that later success is not executed.
- Corrected only the erroneous Windows test expectation; production path conversion is unchanged.
- Pinned production source and embedded text assets to LF checkout. Binary assets stay `text=auto`; raw port fixtures are protected from automatic text conversion. Existing generated JSON LF rules remain intact.
- Reproduced the original digest **exactly** with `git -c core.autocrlf=true checkout-index` in a temporary directory: 460 production inputs, 410 converted before the fix. After attributes: zero changed inputs and the exact original oracle digest. No checksum normalization or oracle relabeling was introduced.
- `python3 scripts/rust-port/test_native_ci_contract.py`: two tests passed, including all five shell failure controls and the real Git checkout regression.
- `actionlint .github/workflows/ci.yml`: passed.
- `GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/portgen --check`: all oracle/fixture checks passed.
- `GOTOOLCHAIN=go1.26.6 go test ./scripts/rust-port/cmd/portgen`: passed.
- `cargo test -p symdesk-protocol --locked`: 34 native macOS tests passed.
- `cargo clippy -p symdesk-protocol --all-targets --all-features --locked -- -D warnings` and `cargo fmt --all --check`: passed.

## Remaining gate

Independent review and actual native Windows CI on the corrected source are pending. The Git checkout regression is a real Git filter reproduction on macOS, **not** native Windows execution. Do not promote RUST-006 or close #895 from these local results alone. Publish the branch after review, dispatch the CI workflow explicitly (PRs skip native Rust), and inspect both step outcomes and raw logs for the exact resulting SHA.
