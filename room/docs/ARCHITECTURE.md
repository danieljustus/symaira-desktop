# Architecture

An overview of the `symroom` signed journal, event types, and hash chains.

---

## Overview

`symroom` is a room coordination layer built around a **signed, append-only journal**. Every action in a room — membership changes, notes, decisions, artifact links, and run lifecycle events — is recorded as a signed event in the journal. The journal is the source of truth from which all derived state (membership, run status, artifact index) is projected.

The design follows these principles:

- **Append-only, per-author segments.** Each author writes to their own `.jsonl` file.
- **Tamper-evident chain.** Within each segment, every line references the sha256 of the previous line.
- **Ed25519 signatures.** Every event carries a signature over its canonical JSON, verifiable against the author's known public key.
- **Derived state.** Membership, runs, checkpoints, and the artifact index are recomputed by replaying journal events. The SQLite index (`index.sqlite`) is a cached projection, rebuilt with `symroom index rebuild`.

---

## Event Structure

Every event is a JSON object with these fields:

| Field | Type | Description |
|-------|------|-------------|
| `v` | int | Schema version (currently `1`) |
| `id` | string | Unique event ID (`ev_` + hex) |
| `room` | string | Room ID (`rm_` + hex) |
| `author` | string | Author member ID (`mem_` + hash) |
| `seq` | uint64 | Per-author monotonic sequence number |
| `prev` | string | SHA-256 hash of the previous line in this author's segment (`sha256:` + hex) |
| `lamport` | uint64 | Lamport clock value for total ordering across authors |
| `ts` | string | ISO 8601 timestamp with millisecond precision (`2006-01-02T15:04:05.000Z`) |
| `kind` | string | Event kind (see below) |
| `body` | object | Event payload (kind-specific) |
| `sig` | string | Ed25519 signature (`ed25519:` + base64) |

Fields are serialised with sorted keys (via `json.Marshal` on a `map[string]any`) to produce a canonical byte representation for signing.

---

## Event Kinds

### Lifecycle

| Kind | Description |
|------|-------------|
| `room.created` | Room initialisation — first event in every room. Body contains `name` and `public_key`. |
| `room.renamed` | Room display name changed. |

### Membership

| Kind | Description | Requires |
|------|-------------|----------|
| `member.added` | A new member was added to the room. Body: `id`, `name`, `public_key`, `role`, `kind`. | Owner |
| `member.removed` | A member was removed. Body: `id`. | Owner |
| `member.role_changed` | A member's role was changed. Body: `id`, `role`. | Owner |

### Notes & Decisions

| Kind | Description |
|------|-------------|
| `note.posted` | A free-form journal note. Body: `text`. |
| `decision.recorded` | A recorded decision with optional references. Body: `text`, `refs` (array of event IDs). |

### Artifacts

| Kind | Description |
|------|-------------|
| `artifact.linked` | A file was linked as a room artifact. Body: `path`, `sha256`, `title`. |
| `artifact.unlinked` | An artifact reference was removed. Body: `path`. |
| `artifact.changed` | An artifact's content hash changed. Body: `path`, `old_sha256`, `new_sha256`. |

### Run Lifecycle

| Kind | Description | Invariants |
|------|-------------|------------|
| `run.requested` | A run was proposed. Body: `run_id`, `title`, `plan_file`, `adapter`. |
| `run.approved` | A human approved the run. Body: `run_id`, `approval_id`, `scope`, `expires_at`. | Agent role forbidden |
| `run.denied` | A human denied the run. Body: `run_id`, `reason`. |
| `run.started` | The run began execution. Body: `run_id`. |
| `run.finished` | The run completed successfully. Body: `run_id`, `summary`, `artifacts`. |
| `run.failed` | The run completed with an error. Body: `run_id`, `error`. |
| `run.cancelled` | The run was cancelled before completion. Body: `run_id`, `reason`. |

### Checkpoints

| Kind | Description | Invariants |
|------|-------------|------------|
| `checkpoint.requested` | A checkpoint was raised during a run. Body: `checkpoint_id`, `run_id`, `question`. |
| `checkpoint.resolved` | A human answered the checkpoint. Body: `checkpoint_id`, `answer`. | Agent role forbidden |

---

## Hash Chains

### Per-author chain

Each author's segment (`journal/<member-id>.jsonl`) is an **append-only, singly-linked hash chain**. Every line in the file is one JSON event followed by `\n`.

The `prev` field in each event contains the sha256 hash of the **entire previous line** (the raw bytes ending with `\n`), or the null hash `sha256:0000...0000` for the first event.

```
null hash ←───────────┐
                      │
Line 1 (room.created) ├──prev──→ hash(line 1) = h₁
                      │
Line 2 (member.added) ├──prev = "sha256:" + hex(h₁) → hash(line 2) = h₂
                      │
Line 3 (note.posted)  ├──prev = "sha256:" + hex(h₂) → hash(line 3) = h₃
```

The hash is computed by `sha256(line_bytes)` and encoded as `sha256:<hex>`. The line includes the trailing `\n`.

`symroom verify` checks every link:

1. For each segment, it reads the file line by line.
2. For each event at position `i`, it verifies that `event.seq == i+1` and `event.prev == hash(line_{i-1})`.
3. Any break (wrong seq, mismatched prev) causes the verification to fail with a `chain_broken` or `seq_mismatch` finding.

### Fork detection

Within a single segment file, duplicate `seq` values with different event IDs are flagged as forks. This detects collisions or concurrent writes to the same segment — a situation that occurs only if the same identity is used on multiple devices writing to the same journal directory.

---

## Signatures

Every event carries an Ed25519 signature over its canonical JSON representation (sorted keys, no `sig` field present during signing).

- **Signing:** `CanonicalBytes(ev)` produces JSON sorted by key. `ed25519.Sign(privKey, canonical)` produces the raw 64-byte signature, encoded as `ed25519:<base64>` in the `sig` field.
- **Verification:** `symroom verify` checks every event against the public key of the author as recorded in the membership state. Unknown authors, bad signatures, or agent-signed approvals are flagged.

### Identity resolution chain

When loading an identity, `symroom` tries the following backends in order:

1. `SYMROOM_IDENTITY_KEY` environment variable (hex-encoded private key or seed)
2. `symvault get symroom/identities/<name>` (when `symvault` is on `PATH`)
3. macOS Keychain (`security find-generic-password -s symroom-identity -a <name>`)
4. File in `~/.local/share/symroom/identities/<name>.json`

---

## Total Order (Lamport Merge)

Events from all authors are merged into a deterministic total order for log display and state projection. The merge sort key is:

1. **Lamport clock** (ascending) — provides a happens-before ordering
2. **Timestamp** (ascending) — tiebreaker for events at the same Lamport value
3. **Author** (ascending) — determinism for same-timestamp events
4. **Seq** (ascending) — within-author ordering
5. **Event ID** (ascending) — final tiebreaker

The Lamport clock is a simple counter: each new event takes `max(global_max, local_max) + 1`. This guarantees that if event A happens before event B, then `A.lamport < B.lamport`.

---

## Derived State

### Membership state

The `members.State` type replays events to derive the current set of room members. Event handlers:

- `room.created ` → registers root owner
- `member.added`  → adds a new member (owner action)
- `member.removed` → removes a member (owner action)
- `member.role_changed` → updates a member's role (owner action)

Role invariants are checked during replay:

- **Agent members cannot approve** — events `run.approved` and `checkpoint.resolved` signed by an agent-role member are invalid.
- **Observer members are read-only** — they cannot create any events.
- **Only owners manage members** — `member.added`, `member.removed`, `member.role_changed` must be signed by a member with role `owner`.

### Run state

The `run.ProjectRuns` function replays run lifecycle events to produce the current state of every run:

```
requested → approved → started → finished
                                 → failed
         → denied
         → cancelled (from requested, approved, or started)
```

### Checkpoints

`run.ProjectCheckpoints` replays checkpoint events, similar to run state.

---

## File System Layout

```
room-root/
├── room.toml                  # Room metadata (shared)
├── .gitignore                 # Ignores .symroom/
├── .symroom/
│   ├── local.toml             # Local config (identity, artifact_root)
│   └── index.sqlite           # Derived SQLite index (local)
├── journal/
│   ├── mem_a1b2c3d4.jsonl     # Alice's segment
│   └── mem_e5f6g7h8.jsonl     # Bob's segment
└── ...                        # Project content (artifacts, etc.)
```

- `room.toml` is the shared room identifier. All participants must have the same `room.toml`.
- `journal/` contains the event segments — one per author.
- `.symroom/` is local-only and **must never be synced**.

---

## Verification Process

`symroom verify` runs a multi-stage check:

1. **Per-segment hash chain verification** — reads each `.jsonl` file, checks `seq` monotonicity and `prev` hash links.
2. **Fork detection** — checks for duplicate `seq` values with different event IDs per author.
3. **Global signature verification** — merges all events into total order, verifies each signature against the public key from the projected membership state.
4. **Membership invariant checks** — applies events to a `members.State` and catches violations (agent approvals, unknown authors, non-owner member management, observer writes).

A report with all findings is produced. If any finding exists, `Valid` is `false`.

---

## Related Documents

- [README.md](../README.md) — installation, quickstart, sync guide, threat model
- [THREAT_MODEL.md](THREAT_MODEL.md) — expanded threat model and attack scenarios
- [approval-contract.md](approval-contract.md) — frozen approval backend interface contract
- [AGENTS.md](../AGENTS.md) — product boundary and convention rules
