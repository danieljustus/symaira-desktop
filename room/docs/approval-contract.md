# Approval Backend Contract

This document freezes the interface and event contract for `symroom` as an approval and work record backend.

---

## Boundaries & Non-Goals

- **No Risk Classification**: `symroom` performs **no risk classification**, no exposure policy evaluation, and no in-data-path tool interception.
- **Bookkeeping Only**: `symroom` is beside the data path — it records who belongs to a room, what runs/checkpoints occurred, and what human approved them.
- **Guard Boundary**: Per-tool-call interception, policy rules, and risk classes belong to `symaira-guard`. `symaira-guard` uses `symroom` as a backend to check whether an operation is approved and record evidence.

---

## Command Line Interface & Exit Codes

### Requesting & Waiting for Approvals

```bash
symroom run request --title <title> [--plan-file <file>] [--adapter <adapter>] [--json]
```

```bash
symroom run wait <run_id> [--timeout 15m] [--json]
```

**Exit Codes:**
- `0`: Approved (`ExitOK`)
- `10`: Denied
- `11`: Timeout

---

## Event Schemas (JSON)

All events are appended to the per-author segment (`journal/<member-id>.jsonl`) and signed with an Ed25519 identity key.

### `run.requested`
```json
{
  "run_id": "run_a1b2c3d4e5f67890",
  "title": "Deploy production database migration",
  "plan_file": "docs/plans/migration.md",
  "adapter": "shell"
}
```

### `run.approved`
```json
{
  "run_id": "run_a1b2c3d4e5f67890",
  "approval_id": "app_1234567890abcdef",
  "scope": "deploy:staging",
  "expires_at": "2026-07-28T16:30:00Z"
}
```

> **Invariant:** Events of kind `run.approved` signed by a member with role `agent` are **invalid** and rejected at append time and journal verification time.

### `run.denied`
```json
{
  "run_id": "run_a1b2c3d4e5f67890",
  "approval_id": "app_1234567890abcdef",
  "reason": "Plan lacks rollback procedure"
}
```

### `checkpoint.requested` & `checkpoint.resolved`
```json
{
  "checkpoint_id": "chk_87654321fedcba09",
  "run_id": "run_a1b2c3d4e5f67890",
  "question": "Apply schema migration to table users?"
}
```
```json
{
  "checkpoint_id": "chk_87654321fedcba09",
  "answer": "Approved for execution"
}
```

> **Invariant:** Events of kind `checkpoint.resolved` signed by a member with role `agent` are **invalid** and rejected.
