# Threat Model

An expanded analysis of what `symroom` protects, what it does not, and the attack scenarios relevant to users and integrators.

---

## Scope

`symroom` is a signed, append-only journal for recording room membership, events, decisions, and run lifecycle. It is **not a security boundary**. It provides **tamper evidence** for the work record — not **access control**, **runtime policy enforcement**, or **sandboxing**.

---

## What symroom Protects

### 1. Later falsification of the work record

Every journal event is Ed25519-signed by its author and linked into a per-author SHA-256 hash chain. This makes it computationally infeasible to:

- **Insert** a forged event into an existing journal segment (wrong `prev` hash breaks the chain).
- **Delete** an event from a segment (subsequent `prev` hash references the deleted line's hash).
- **Reorder** events within a segment (sequence numbers and prev hashes both become invalid).
- **Modify** an event in place (signature becomes invalid, `prev` hash of the next event breaks).

`symroom verify` checks every signature and every hash link. Any break is reported as a finding and the journal is marked invalid.

**Detection capability:** A single honest verifier running `symroom verify` against the journal directory will detect any of the above tampering attempts.

### 2. Unattended agent runs

Agent-role members cannot approve their own runs or resolve their own checkpoints:

- **Append-time rejection:** `symroom run approve` and `symroom run resolve-checkpoint` reject events signed by an agent identity before writing.
- **Verify-time detection:** `symroom verify` flags any `run.approved` or `checkpoint.resolved` event signed by an agent-role member (`code: agent_approval_forbidden`).
- **Role matrix:**

| Event Kind | Allowed Roles |
|---|---|
| `run.requested` | owner, member, agent |
| `run.approved` | owner, member |
| `run.denied` | owner, member |
| `checkpoint.requested` | owner, member, agent |
| `checkpoint.resolved` | owner, member |
| `note.posted` | owner, member, agent |
| `member.added` | owner |

This ensures that a human (owner or member role) must always explicitly approve a run before it can start. See [ARCHITECTURE.md](ARCHITECTURE.md#membership-state) for the full role matrix.

---

## What symroom Does NOT Protect

### 1. Malicious agent acting outside the room

`symroom` only records events that are written to its journal. If an agent has filesystem access, network access, or any other capability outside of `symroom`, the room journal cannot prevent it from using those capabilities.

**Example attack:** An agent with `agent` role in the room uses its direct shell access to delete files on disk. The deletion is not recorded as a `room` event. The journal shows the agent requested a run and a human approved it — but the agent's destructive action happened in the gap between approval and start, outside the room scope.

**Mitigation:** This is the responsibility of `symguard`, which intercepts every tool call at runtime and evaluates it against policy. See [AGENTS.md](../AGENTS.md#room--guard-boundary-do-not-weaken).

### 2. Compromised owner key

The room owner can add and remove members, change roles, and approve runs. If the owner's private key is compromised:

- The attacker can add new members (including new "owner" members).
- The attacker can approve any run.
- The attacker can remove legitimate members.
- The existing journal history remains tamper-evident — the attack is detectable — but `symroom` cannot undo the damage.

**Mitigation:** Key hygiene — store identities in a vault (`symvault`), use the macOS Keychain, or use hardware-backed key storage. The owner's private key is the root of trust for the room.

### 3. Device with a never-written journal

A participant who has an identity and access to the room directory but never writes events to their segment file leaves no trace. `symroom` cannot verify what was not recorded.

**Example:** An agent is added as a member but configured to operate silently without posting events. The first time it writes an event, that event will appear as seq=1 in its segment, but prior activity is invisible.

### 4. Pre-key-compromise events

If a private key is compromised, past events' signatures can be verified against the public key still, but future events can be forged under the same identity. There is no key rotation mechanism (room re-initialisation is the current workaround).

---

## Equivocation

### What equivocation is

Equivocation is when a single participant publishes different statements to different audiences. In the `symroom` context, this means one identity writing conflicting events to different journal segments (e.g., on two devices that both append to the same journal directory using the same private key).

### How symroom handles it

**Equivocation is detected, not prevented.**

- Within a single segment file, fork detection catches duplicate `seq` values with different event IDs.
- Across separate segment files (different devices with different identities), each author's chain is independent — the Lamport-clock total order includes all events from all authors.

**Example equivocation scenario:** Alice has her private key on both her laptop and her desktop. Both machines append to the same synced journal directory. The laptop writes `note.posted` at seq=5. The desktop writes `note.posted` at seq=5 using the same key. The desktop's file overwrites or conflicts with the laptop's. `symroom verify` detects the duplicate seq or broken hash chain and reports a fork.

### The correct mitigation

**One identity per device.** Each machine gets its own Ed25519 identity with its own member ID. The owner adds each device's identity as a distinct member. Now each device writes to its own segment file, no fork can occur, and all events are independently verifiable.

---

## Attack Scenarios

### Scenario A: Tampering with the journal (outsider)

| Step | Action | Detection |
|------|--------|-----------|
| 1 | Attacker gains read/write access to the journal directory | — |
| 2 | Attacker deletes line 3 from `mem_alice.jsonl` | `verify` reports broken hash chain at line 4 (prev hash mismatch) |
| 3 | Attacker inserts a forged event into `mem_bob.jsonl` | `verify` reports signature invalid (no valid key for forged event) |

**Verdict:** Detected. The hash chain and signatures protect journal integrity even when the attacker has write access to the files.

### Scenario B: Agent run approval bypass

| Step | Action | Detection |
|------|--------|-----------|
| 1 | Agent tries to sign its own `run.approved` | Append-time rejection by `symroom run approve` |
| 2 | Agent writes a `run.approved` event directly into the JSONL file | `verify` reports `agent_approval_forbidden` |

**Verdict:** Prevented at append time; detected by `verify` if bypassed.

### Scenario C: Compromised owner

| Step | Action | Detection |
|------|--------|-----------|
| 1 | Attacker steals owner's private key | — |
| 2 | Attacker adds a new member with role `owner` and kind `human` | Event is signed by a valid owner key — verification passes |
| 3 | Attacker approves a malicious run under the new identity | Verification passes |
| 4 | Later, the real owner notices the unauthorised member in `symroom log` | Detection through audit, not cryptography |

**Verdict:** Not prevented cryptographically. Detectable through journal audit. The existing chain of events remains tamper-evident for forensic analysis.

### Scenario D: Multi-device identity fork

| Step | Action | Detection |
|------|--------|-----------|
| 1 | User copies `alice.json` identity file to two laptops | — |
| 2 | Both laptops append to the same synced journal directory | — |
| 3 | First laptop writes seq=5, second laptop also writes seq=5 | Conflict in the shared file |
| 4 | `symroom verify` detects duplicate seq or broken chain | `fork_detected` finding |

**Verdict:** Detected. Mitigated by one identity per device.

---

## Security Invariants (Summary)

1. **Every event has a verifiable Ed25519 signature** from a member whose public key is recorded in the membership state.
2. **Every event chain is a linked hash list** — each event's `prev` is the sha256 of the previous line.
3. **Agent role cannot approve** — `run.approved` and `checkpoint.resolved` by agents are invalid.
4. **Observer role is read-only** — observers cannot write any events.
5. **Only owners manage membership** — membership changes must be signed by an owner.
6. **Duplicate seq with different event IDs = fork** — detected but not prevented.
7. **`symroom` does not enforce at runtime** — verification is a separate audit step.

---

## Related Documents

- [ARCHITECTURE.md](ARCHITECTURE.md) — signed journal, event types, hash chains
- [README.md](../README.md) — quickstart, sync guide, threat model summary
- [AGENTS.md](../AGENTS.md) — product boundary rules (Room ↔ Guard/Brain/Desk)
