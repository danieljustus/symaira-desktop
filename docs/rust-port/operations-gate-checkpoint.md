# Per-operation value gate continuation

## Scope and decision

The migration remains in progress, not complete. The renewed request authorizes completion of the existing DAG without changing the 10% p95 regression ceiling, deleting Go, or publishing a release. RUST-006 must pass current acceptance and native parity before dependent work is approved.

## Evidence

- Measured clean candidate: `5088972aa7efadfdc7118549354e26d001c1ffad` on local macOS.
- Command: `PYTHONDONTWRITEBYTECODE=1 GOTOOLCHAIN=go1.26.6 make value-001 VALUE_OUTPUT=/tmp/symdesk-value001-5088972a.json`.
- Release build completed. Generated 10,000-document surrogate; 100 samples and 20 warmups. This is not user-vault data.
- Artifact reports passing contracts, size/RSS improvement and every required aggregate and individual MCP/HTTP latency gate. Independent acceptance validation is pending; historical passing captures do not certify a changed candidate.
- Privacy-derived capture: `results/value001-operations-5088972a.json`; provenance and exact redacted field inventory: `results/value001-operations-5088972a.metadata.json`. Original raw capture is preserved outside the repository; no measurements or identities were changed.
- The old 956bd3e resumption capture is genuine but fails individual file-read/file-missing latency gates. It must remain a negative control, not a required passing approval command.
- Native run for Rust-equivalent test repair `0159d80013b7cab1654d1fb7cb29e3d40be2324b`: https://github.com/danieljustus/symaira-desktop/actions/runs/34314724745. Latest observed Ubuntu native suite and representative parity pass; Windows workspace and representative parity pass, final sidecar/version checks still pending. This run does not certify subsequent harness changes.

## Revalidating the reviewed capture

Run from this checkout with a separate clean candidate checkout. This verifies
5088972a, not the newer checkout containing the validator and documentation.

```sh
git worktree add --detach ../value-508-check 5088972aa7efadfdc7118549354e26d001c1ffad
PYTHONDONTWRITEBYTECODE=1 make value-001-validate \
  VALUE_OUTPUT=docs/rust-port/results/value001-operations-5088972a.json \
  VALUE_CANDIDATE=5088972aa7efadfdc7118549354e26d001c1ffad \
  VALUE_CANDIDATE_ROOT=../value-508-check \
  VALUE_TRUSTED_SHA256=70e2c2401f8acb4ea40b5a3e57b108a5cd18ff96be7a6a5823ff6a7e0f66d5ac
```

If the candidate checkout already exists, verify its HEAD and clean status;
do not recreate or overwrite it. The raw and derived captures were independently
compared recursively; only the nine documented private-path fields differ.
The exact-candidate validator and capture passed independent review at 8c9e83b7.
Full native run 34314724745 completed successfully on 0159d800.

## Owned work and next actions

- `fix/value-operation-gates`: integrated per-operation producer/validator changes plus Windows fixture repair; capture files and this checkpoint currently uncommitted.
- `fix/windows-rust-gates`: PR #896, current remote head `0159d80013b7cab1654d1fb7cb29e3d40be2324b`.
- SymRoom lifecycle work preserved at `cbdd1c54be53856fb608391722843b485d499148` in `migration/symroom-actions-recovery`; overall RUST-016 remains partial.
- Issue #897 tracks acceptance defects; issue #895 tracks Windows gates.
- Independent validator implementation is delegated to `deleg_46f46923`, isolated managed worktree, new candidate-validator/test files only. Verify returned source and tests before integrating; session workers are not durable after session termination.
- Next: integrate and review exact-candidate acceptance validator; split Make/CI historical evidence checks from current approval; validate fresh capture against its explicit measured commit and trusted digest; re-run affected checks; verify exact-head native results; then reconcile RUST-006 and resume the existing DAG. Do not mark completion based on benchmark JSON alone.
