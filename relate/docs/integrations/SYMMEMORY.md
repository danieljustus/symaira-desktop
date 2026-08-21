# SymMemory integration

`symrelate` can optionally link a person or organization to a SymMemory
entity so a user can pull up related context without SymMemory ever
becoming a requirement, and without any contact-point data ever being
sent to it. This document is the status report for that integration —
what is implemented, what was verified against a real installation, and
what is still blocked on upstream work.

See [ARCHITECTURE.md](../../ARCHITECTURE.md)'s standalone-first rule: no
package in this repository imports a SymMemory package. Everything below
talks to a `symmemory` binary on `PATH` via subprocess calls, exactly like
`internal/domain/security.SymVaultKeyProvider` does for SymVault.

## What's implemented

- **Runtime discovery** (`internal/integration/symmemory.Discover`): a
  bounded (3s default, configurable), `PATH`-based handshake via
  `symmemory version --json`. Missing binary, timeout, non-zero exit,
  malformed JSON, or a `tool` field that isn't `"symmemory"` all degrade
  to `ErrUnavailable` — `symrelate` never fails to build, run, or pass its
  tests without SymMemory installed.
- **Reviewed candidate selection** (`internal/integration/symmemory.Candidates`):
  `symrelate memory candidates --query <name> [--type person] [--aliases a,b]
  [--limit N]` shells out to `symmemory entity resolve ... --output json` and
  returns scored, explainable candidates (exact/alias/normalized match kinds).
  The user reviews the list and then links the chosen entity id explicitly —
  no automatic match is ever persisted.
- **Reviewed external links** (`internal/domain/memorylink`,
  `internal/service/memorylink`, migration `0006_memory_links.sql`): a
  Relate-owned `memory_links` table mapping a person/organization to a
  SymMemory entity id. Linking is always explicit — `symrelate memory
  link --person <id> --entity <symmemory-entity-id>` — the user must have
  already selected the entity id (from the candidates list or elsewhere);
  nothing here matches automatically. At most one link per contact;
  relinking requires an explicit unlink first, so an existing link is never
  silently overwritten.
- **Context lookup** (`internal/integration/symmemory.Context`): given an
  already-linked entity id, shells out to `symmemory entity show <id>
  --output json` and returns the entity's own JSON representation for
  display — `symrelate` does not parse, store, or interpret its fields.
- **Provenance-aware relation writes** (`internal/integration/symmemory.Relate`):
  `symrelate memory relate --person <id> --relation <type> --to-id <entity-id>
  [--verification verified|unverified] [--evidence-json <json>]` reads the
  contact's linked entity id and writes a directed relation in SymMemory via
  `symmemory entity relate --from-id ... --to-id ... --source symrelate
  --source-ref <contact-id> --verification ... --output json`. The relation is
  idempotent in SymMemory by source/source-ref/relation triple. Contact-point
  data is never sent. `symrelate memory unrelate --relation-id <id>` removes
  a relation by its stable SymMemory id.
- **CLI surface**: `symrelate memory status|candidates|link|unlink|relate|unrelate|show`
  — see each command's `-h` output.

## Verified against a real installation

While building this, a real `symmemory v0.12.0` was available locally,
which let two assumptions get checked against actual behavior instead of
staying guesses:

1. `symmemory version --json` returns exactly `{"tool":"symmemory",
   "version":"...","schema_version":N}` — confirmed correct.
2. `symmemory entity show <arg> --output json` prints the entity as one
   JSON object **followed by non-JSON "Linked memories" text on the same
   stdout stream**. `Context()` was originally written to validate the
   *entire* stdout as JSON, which would have rejected every real,
   successful response — a decode-one-value streaming parser (rather than
   whole-output validation) is required, and that is what's implemented
   and tested (`TestContext_TrailingNonJSONText_IsIgnored`).

`internal/integration/symmemory/discover_test.go` also has a light
integration check (`TestContext_AgainstRealBinary`) that only runs when a
real `symmemory` is on `PATH` (skipped in CI, per the standalone-first
rule — no other Symaira tool is present there) so a developer with
SymMemory installed gets an early signal if the real CLI's output shape
drifts from what this package assumes.

## Known limitation, confirmed against that real installation

`entity show` resolves its argument **by name, not by id**. `memory_links`
deliberately stores the entity's stable `id` (never a mutable name), so a
link survives the linked entity being renamed in SymMemory — but that
means `symrelate memory show` calls `Context()` with an id that the real
`entity show` command cannot resolve today, and gets the safe, expected
`ErrUnavailable`. Candidate selection and relation writes use the newer
`entity resolve` and `entity relate` commands, which are id-aware, so
`memory show` is the only remaining consumer of `entity show` and is
documented as gracefully degraded when SymMemory does not resolve by id.

(End of file - total 93 lines)
