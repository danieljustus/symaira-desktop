# Per-operation value gate continuation

## Current acceptance (2026-09-18, schema 6)

RUST-006 is **passed** for the exact candidate
`5c5e98c5aab2df54bb2c47ad51fe3d2d8e71f23b` (clean worktree) against the unchanged
Go oracle `745c08e8144971c61133c5d0e5d61c7ce405aad2`.

- One fresh schema-6 run: 100 samples / 20 warmups, generated
  10,000-document surrogate, every HOME/XDG/TMP/Go/Cargo path on the attached
  NVMe.
- Private raw capture SHA-256
  `40267a5c47cf67579a708e9b0e61425960872d04a1f08796994ef157fd758a63` (kept
  outside the repository); published redaction `results/value001-5c5e98c5.json`
  SHA-256 `a67779054c210d88212bf41fd934e4127ed0b5807c8610c1fc70934359619e5d`,
  with `results/value001-5c5e98c5.metadata.json` recording the nine redaction
  paths and the trusted CI run `35354149053`.
- Verdict: contracts pass (all four differential commands exit 0), improvement
  passes (binary size −84.80 %, representative RSS −71.62 %), latency passes
  under the schema-6 interval rule.
- Independent re-derivation: `python3 scripts/rust-port/validate_value001_5c5e98c5.py --raw <private capture>`
  proves the published file is exactly the reviewed redaction of the raw capture
  — every other value byte-identical, and no private path in the published file.
- Decision rule (owner-approved, PR #956): a cohort fails only when its 95 %
  order-stratified interval lower bound exceeds the +10 % ceiling, and the run
  fails when an interval is wider than `0.40` (it then cannot exclude a
  regression of three times the ceiling). The ceiling itself is unchanged; the
  schema-5 point-estimate rule and every earlier capture stay historical.
- Fixes that made this run possible: #950 (representative hang guard 10s→30s,
  any timeout now fails), #951 (index-preparation failures name the side), #953
  (Makefile quoting for isolated/spaced Cargo paths), #954 (timed MCP list call
  scoped to each side's own absolute cohort directory), #956 (schema-6 interval
  rule). Evidence trail and the two earlier aborts: #936, #952, #955.

RUST-007 and RUST-016 become `ready`; later items stay `blocked` until their
dependencies pass. Go remains production: no release, no cutover, no Go removal.

## Scope and decision

The migration remains in progress, not complete. The renewed request authorizes completion of the existing DAG without changing the 10% p95 regression ceiling, deleting Go, or publishing a release. RUST-006 must pass current acceptance and native parity before dependent work is approved.

> **Current-candidate supersession (2026-09-16):** Three fresh full VALUE-001
> runs at `fbc52d0ca07a9bbca324a342263ad86dd05adf52` produced FAIL/PASS/FAIL.
> The systematic alternating HTTP-order bias is tracked in
> [#936](https://github.com/danieljustus/symaira-desktop/issues/936); therefore
> RUST-006, RUST-007, and RUST-016 are blocked. See
> [`value001-fbc52d0c-current-gate.md`](value001-fbc52d0c-current-gate.md).
> The historical evidence below remains archival and does not override this
> current DAG decision.

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

## Schema-4 review rework checkpoint (2026-09-16)

The candidate validator now explicitly rejects boolean `binaries.*.bytes` metadata (Python `bool` is an `int` subclass), with negative coverage for both current Go and Rust binary entries. `PYTHONDONTWRITEBYTECODE=1 make value-001-evidence-tests` passes 150/150; no fresh benchmark or native build was run, and RUST-006 remains blocked pending independent review and the exclusive current measurement.
