# symroom

> **Moved 2026-08-21** into `symaira-desktop/room/` as part of the Symaira
> repo consolidation (27 → 13, `docs/repo-konsolidierung.md` §3.2). This Go
> module (plus Swift client package) now lives in symaira-desktop; the
> original `danieljustus/symaira-room` repo remains active until the deep
> integration is done.

[![CI](https://github.com/danieljustus/symaira-room/actions/workflows/ci.yml/badge.svg)](https://github.com/danieljustus/symaira-room/actions/workflows/ci.yml)
[![Go version](https://img.shields.io/github/go-mod/go-version/danieljustus/symaira-room)](https://go.dev/dl/)
[![License](https://img.shields.io/github/license/danieljustus/symaira-room)](LICENSE)

![symroom demo](docs/demo.svg)

`symroom` is the shared, verifiable work record of a project: who belongs to it, what happened, and what was approved. It is a signed, append-only journal of events (notes, decisions, membership changes, run lifecycle) — not a chat system, not a policy engine, and not a Git host.

**Repository:** [github.com/danieljustus/symaira-room](https://github.com/danieljustus/symaira-room)

> **Stability:** `symroom` is pre-1.0 (`v0.x`). The CLI surface and journal format may change until `v1.0`; see the [roadmap](#roadmap).

---

## Installation

Requires Go 1.26+ (or Homebrew on macOS).

```sh
go install github.com/danieljustus/symaira-room/cmd/symroom@latest
```

Or via Homebrew (macOS):

```sh
brew install danieljustus/tap/symroom
```

Or build from source:

```sh
git clone https://github.com/danieljustus/symaira-room
cd symaira-room
make build          # produces bin/symroom
go build -o /usr/local/bin/symroom ./cmd/symroom   # or install directly
```

Run `make test` to execute the full test suite.

---

## Roadmap

- **v0.x (current):** core room + journal + members + runs + approvals; MCP server; `symbrain` profile emission. API and journal format may change.
- **v1.0:** frozen journal format and CLI exit-code contract; long-term stability guarantees.

## Quickstart

This walkthrough creates a room, adds a member, posts notes and artifact links, requests a run, approves it, and views the journal.

### 1. Init

Create a fresh, empty directory and initialise the room inside it. The directory must be empty — `symroom init` refuses to overwrite existing content.

```sh
mkdir my-project && cd my-project
```

### 2. Create identities

Every participant needs an Ed25519 identity. Identities are stored in `~/.local/share/symroom/identities/` (or `$XDG_DATA_HOME/symroom/identities/`).

```sh
symroom identity create alice    # Created identity alice (mem_<hash>)
symroom identity create bob      # Created identity bob (mem_<hash>)
symroom identity create builder  # Created identity builder (mem_<hash>)
```

List identities:

```sh
symroom identity list
```

### 3. Init the room

Create the room as `alice`, who becomes the owner. The `--name` flag gives the room a human-readable display name.

```sh
symroom init . --identity alice --name "My Project"
# Initialized room rm_<id> in . (owner: alice)
```

This writes:

- `room.toml` — room ID, creation timestamp, root public key
- `journal/mem_<alice-hash>.jsonl` — the signed `room.created` event
- `.symroom/local.toml` — local machine config (never synced)
- `.gitignore` — excludes `.symroom/`

### 4. Add members

Only the room owner (`alice`) can add members. First, export the public key of the member to add:

```sh
symroom identity export bob --public
# <hex-encoded public key>
```

Then, as owner, add them:

```sh
symroom member add \
    --identity alice \
    --pubkey <bob-hex-key> \
    --name bob \
    --role agent \
    --kind agent
# Member added: bob (mem_<bob-id>, role: agent, kind: agent, event: ev_<event-id>)
```

The `--identity` flag selects the signing identity and must be the room owner; it falls back to `default_identity` from the config when omitted. The positional form `symroom member add <name> <pubkey>` is equivalent. Members can have roles: `owner`, `member`, `agent`, or `observer` — and kinds: `human` or `agent`.

List members, change a role, or remove a member (all owner-only):

```sh
symroom member list
# ID                       NAME    ROLE    KIND
# mem_<alice-hash>         alice   owner   human
# mem_<bob-hash>           bob     agent   agent

symroom member list --json
# [
#   {
#     "id": "mem_<alice-hash>",
#     "name": "alice",
#     "role": "owner",
#     "kind": "human"
#   },
#   {
#     "id": "mem_<bob-hash>",
#     "name": "bob",
#     "role": "agent",
#     "kind": "agent"
#   }
# ]

symroom member role --identity alice mem_<bob-hash> member
symroom member remove --identity alice mem_<bob-hash>
```

`member list --json` prints the members as a machine-readable JSON array (fields `id`, `name`, `role`, `kind`, sorted by ID) — the input format used by the [Swift client](#swift-client-symroomclient).

| Role       | Can approve runs? | Can post notes? | Can manage members? |
|------------|:---:|:---:|:---:|
| owner      | ✓   | ✓   | ✓   |
| member     | ✓   | ✓   |     |
| agent      | ✗   | ✓   |     |
| observer   | ✗   |     |     |

> **Hard invariant:** Members with role `agent` are forbidden from signing approval events (`run.approved`, `checkpoint.resolved`). This is enforced at append time and verified by `symroom verify`.

### 5. Post a note or link an artifact

Record what happened:

```sh
symroom note "kicked off the project" --identity alice
# ev_<event-id>

symroom decide "adopted the new API design" --refs ev_abc123 --identity alice
# ev_<event-id>
```

Link a file as a room artifact (relative path + sha256 content hash):

```sh
echo "# Design" > DESIGN.md
symroom artifact link DESIGN.md --title "Design doc" --identity alice
# ev_<event-id>
```

### 6. Request a run

An agent (`bob`) can request a run with a title and an optional plan file:

```sh
symroom run request \
    --title "Implement authentication" \
    --plan-file docs/plans/auth.md \
    --adapter shell \
    --identity bob
# run_<run-id>
```

### 7. Approve the run

A human (`alice`) approves the run:

```sh
symroom run approve <run-id> --scope "auth:impl" --identity alice
```

Then start it:

```sh
symroom run start <run-id> --identity bob
```

### 8. View the journal

```sh
symroom log
# [2026-07-30T12:00:00.000Z] mem_<alice> (room.created): ...
# [2026-07-30T12:01:00.000Z] mem_<alice> (member.added): ...
# [2026-07-30T12:02:00.000Z] mem_<alice> (note.posted): ...
# [2026-07-30T12:03:00.000Z] mem_<alice> (artifact.linked): ...
# [2026-07-30T12:04:00.000Z] mem_<bob> (run.requested): ...
# [2026-07-30T12:05:00.000Z] mem_<alice> (run.approved): ...
```

Filter by kind, author, or time range:

```sh
symroom log --kind run.requested --author <author-id> --since 2026-07-01T00:00:00Z
```

Verify the journal integrity:

```sh
symroom verify
# Journal verification PASSED: zero findings
```

---

## Threat Model

### What symroom protects against

1. **Later falsification of the work record.** Every event in the journal is signed by its author. For each author, the per-segment hash chain ensures that no event can be inserted, deleted, or reordered without detection. `symroom verify` checks every signature and every link in every hash chain.

2. **Unattended agent runs.** Agent-role members cannot approve their own runs or resolve their own checkpoints — the journal rejects those events at append time. Every `run.requested` must be followed by a `run.approved` signed by a human (owner or member role) before the run can start.

### What symroom does NOT protect against

- **A malicious agent acting outside the room.** If an agent has filesystem or network access independent of `symroom`, the room journal cannot prevent it from using that access. Preventing out-of-band actions is the job of `symguard`, which sits in the data path and intercepts every tool call.

- **A compromised owner key.** The room owner has full membership-management powers. If the owner's private key is stolen, an attacker can add their own members, approve their own runs, and rewrite the membership state forward. The existing journal history remains tamper-evident — the attack is detectable — but cannot be undone by `symroom` alone.

- **A malicious device that never writes to the journal.** `symroom` only sees what was signed and appended. A participant who performs work without recording it leaves no trace for `symroom` to verify.

> **symroom does not "secure your agents."** It secures the *record of work* inside the room. Agent-behaviour enforcement at the tool-call boundary belongs to [symguard](https://github.com/danieljustus/symaira-guard).

---

## Sync Guide

### Room directory placement

The room directory (the directory you run `symroom init` in) can live:

- **Anywhere on your local filesystem.** This is the simplest setup for a single-machine project.
- **Inside a synced vault** (e.g. iCloud Drive, Dropbox, Obsidian Sync, Syncthing). Because the journal is append-only JSONL, concurrent writes from different devices will not collide — each author writes to their own segment file.
- **Inside a Git repository.** The journal files (regular JSONL) version control naturally, though the primary verification mechanism is cryptographic, not Git-based.

### What must never be synced

**Never sync the `.symroom/` directory.** It contains:

- `local.toml` — the local machine's identity binding (`identity = "..."`, `artifact_root = "..."`)
- `index.sqlite` — a local, derived SQLite index rebuilt from the journal

Syncing `.symroom/` would leak the identity-name ↔ member-ID binding across machines, which is a privacy concern and can cause stale-index conflicts.

`symroom init` creates a `.gitignore` that ignores `.symroom/` automatically.

### One identity per device (and why)

**Each device should have its own named identity.** Do not copy a private key file across machines.

- The member ID is derived from the Ed25519 public key (`mem_<sha256(pubkey)[:8]>`). Every device with its own identity produces a distinct, verifiable signature.
- **Equivocation is detected, not prevented.** If the same private key signs events from two different journal files (e.g. two copies of the same identity on two devices), `symroom verify` cannot detect a fork because the events belong to separate segments (same author name, but colliding seq/prev hash). The correct mitigation is one identity per device, so that each device's events form their own independently verifiable chain.
- When you need a new device, create a fresh identity with `symroom identity create <name>-<device>` and have the room owner add it as a member.

### Multi-device workflow

```
Device A (alice-mac)    →  journal/mem_a1b2.jsonl
Device B (builder-linux) →  journal/mem_c3d4.jsonl
```

The sync layer (iCloud, Git, Syncthing, etc.) merges independent `.jsonl` segments. Each device appends only to its own segment. The `symroom log` command merges all segments into a deterministic total order using Lamport clocks and timestamps.

---

## Boundaries

`symroom` is one tool in the Symaira ecosystem. For full product-boundary descriptions, see [AGENTS.md](AGENTS.md). The key boundaries are:

| Concern | Belongs to |
|---|---|
| Work record — who belongs, what happened, what was approved | **symroom** (this repo) |
| Per-tool-call enforcement, risk classification, policy evaluation | **symguard** (symaira-guard) |
| Content ownership, vault management, file artifacts | **symdesk** (symaira-desktop) |
| Agent capability exposure, brain profiles | **symbrain** (symaira-brain) |

- **Room ↔ Guard:** `symroom` records approvals and runs; `symguard` evaluates every tool call against policy at runtime. See [AGENTS.md](AGENTS.md#room--guard-boundary-do-not-weaken) for the detailed boundary contract.
- **Room ↔ Brain:** `symroom` emits a `symbrain` profile for agent members — it never evaluates exposure policy.
- **Room ↔ Desk:** `symroom` owns references (relative path + sha256); `symdesk` owns the actual content. `symroom` never writes into the vault.

---

## Data Layout

```
my-project/
├── room.toml             # Room ID, creation timestamp, root pubkey
├── .gitignore            # Ignores .symroom/
├── .symroom/
│   ├── local.toml        # Local identity binding (never synced!)
│   └── index.sqlite      # Derived SQLite index (local)
├── journal/
│   ├── mem_<a>.jsonl     # Alice's signed events (append-only)
│   └── mem_<b>.jsonl     # Bob's signed events (append-only)
└── ...                   # Your project files
```

- **`room.toml`**: TOML-format room metadata — schema version, room ID, creation timestamp, root Ed25519 public key, root event ID.
- **`journal/<member-id>.jsonl`**: One append-only, per-author JSONL file. Every line is a signed event. The per-author hash chain (each event references the sha256 of the previous line via `prev`) makes tampering detectable. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for details.
- **`.symroom/local.toml`**: TOML-format local machine config — default identity name, artifact root path. **Never sync this.**
- **`.symroom/index.sqlite`**: A local, derived SQLite index rebuilt with `symroom index rebuild`. Contains a SQL view of the journal for fast queries. Rebuild any time after a sync merge.

---

## Exit codes

`symroom` uses stable exit codes for scripting (defined in the shared `symaira-corekit/exitcodes` package):

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Generic error |
| 2 | Missing or invalid input |
| 3 | Authentication failure |
| 4 | Operation not permitted (e.g. a run was denied) |
| 5 | Resource not found |
| 6 | Conflict with current state |
| 7 | Internal software error |
| 8 | Data format error |
| 9 | Configuration error |
| 10 | Interrupted (e.g. `run wait` / `checkpoint wait` timed out) |

Scripts should check these codes rather than matching stderr text. Codes are stable for the v0.x line; see the `exitcodes` package for the canonical list.

---

## Swift Client (SymroomClient)

A Swift package for macOS 14+ integrations lives in [`client/`](client/). It consumes the `symroom` CLI — it renders `--json` output and never reimplements room logic. The package is not published to a separate registry; reference it as a local path dependency:

| Product | Purpose |
|---|---|
| `SymroomKit` | CLI bridge (`RoomCLIClient`) plus Codable models for members, runs, checkpoints, and journal events. |
| `SymroomFeature` | SwiftUI views and state for the Symaira Hub (room dashboard, participants, journal, approvals). |

```swift
// swift-tools-version:6.0
import PackageDescription

let package = Package(
    name: "MyIntegration",
    dependencies: [
        .package(path: "../symaira-room/client"),
    ],
    targets: [
        .target(
            name: "MyIntegration",
            dependencies: [
                .product(name: "SymroomKit", package: "SymroomClient"),
            ]
        ),
    ]
)
```

Minimal usage — read the member list as JSON:

```swift
import SymroomKit

let client = RoomCLIClient()
let members = try await client.listMembers(in: "/path/to/room")
for member in members {
    print("\(member.name): \(member.role)")
}
```

`RoomCLIClient` locates the `symroom` binary on `PATH` (or Homebrew paths) and runs it with `SYMROOM_ROOM_DIR`; see [`client/`](client/) for the full API (journal, runs, checkpoints).

---

## Development

```sh
make build       # bin/symroom
make test        # go test -v ./...
make test-race   # go test -race -v ./...
make lint        # gofmt -s -w . && go vet ./...
```

The CI pipeline (see [ci.yml](.github/workflows/ci.yml)) runs `gofmt` check, `go vet`, build, tests with the race detector, and `govulncheck` on every push/PR to `main`.

### Further reading

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — signed journal, event types, hash chains
- [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) — expanded threat model with attack scenarios
- [docs/approval-contract.md](docs/approval-contract.md) — frozen approval backend interface contract
- [AGENTS.md](AGENTS.md) — full product boundary and convention rules

---

## License

Apache 2.0. See [LICENSE](LICENSE).
