# Local completion of the Rust port

Decided 2026-10-30. The remaining migration is finished locally on the MacBook
and reaches GitHub once it is complete. Per-slice pull requests and per-slice
hosted CI runs are suspended.

## Rules

1. **Branches.** Integration happens on local `main`. Slices are developed on
   `local/<slug>` branches in worktrees under
   `Symaira_Dev/Worktrees/symaira-desktop/` and merged into local `main` after
   their gate. Backup pushes of `local/**` branches are allowed: no workflow
   triggers on them (CI runs only for `main`, tags and pull requests).
2. **Gate.** A slice is integrated only after
   `scripts/rust-port/local-gate.sh all [targets]` exits 0 on a clean tree.
   `macos` runs natively on Darwin/arm64, `linux` in the Ubuntu 24.04 arm64
   container from `scripts/rust-port/local-gate/Dockerfile` (pins mirror
   `.github/workflows/ci.yml`). Default targets are
   `rust-check rust-lint rust-test port-contract`; a slice adds its own
   differential targets. Every run appends a receipt to
   `target/local-gate/receipts.jsonl`.
3. **Matrix wording.** A row may be promoted with the label `PASS (local)`,
   naming the SHA and both receipts. Never claim hosted CI that did not run.
   The single CI run of the closing push confirms or corrects these rows.
4. **Scope.** Windows and macOS Intel are out of scope. The cutover is
   Rust-only (see the cutover decision in `README.md`).

## Known limits

- Linux runs on arm64, hosted CI on x86_64. Architecture-dependent defects
  surface only in the closing CI run.
- Findings accumulate until the closing push instead of surfacing per slice.
