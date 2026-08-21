# SymMeet integration

`symrelate` can turn a reviewed SymMeet meeting into a contact interaction
— an opaque meeting reference on a person or organization's timeline,
never a transcript, recording, or automatically-matched participant list.
This document is the status report for that integration.

See [ARCHITECTURE.md](../../ARCHITECTURE.md)'s standalone-first rule: no
package in this repository imports a symaira-meet package. Discovery
talks to a `symmeet` binary on `PATH` via subprocess calls, the same
pattern [SYMMEMORY.md](SYMMEMORY.md) documents for SymMemory.

## What's implemented

- **Runtime discovery** (`internal/integration/symmeet.Discover`): a
  bounded `symmeet capabilities --json` handshake. Missing binary,
  timeout, non-zero exit, malformed JSON, or a `tool` field that isn't
  `"symmeet"` all degrade to `ErrUnavailable` — `symrelate meeting status`
  reports availability without ever failing the command, and every other
  `symrelate` command is entirely unaffected by SymMeet's presence or
  absence.
- **Idempotent meeting interactions**
  (`internal/service/relationship.ImportPersonMeeting` /
  `ImportOrganizationMeeting`, migration
  `0007_meeting_idempotency.sql`): reuses the existing `meeting`
  `InteractionKind` and `ExternalRef` field already in
  `internal/domain/relationship` — no new domain concept was needed here,
  only the idempotency guarantee. Re-importing the same meeting id for
  the same person/organization returns the existing interaction (verified
  by `TestImportPersonMeeting_ReImport_IsIdempotent`) instead of creating
  a duplicate, enforced at the database level with a unique partial index
  (not just a service-layer check) so a concurrent double-import can't
  race past it either.
- **CLI surface**: `symrelate meeting status` and `symrelate meeting
  import --id <meeting-id> [--title ...] [--summary ...] [--occurred
  RFC3339] (--person <id>[,...] | --org <id>[,...]) [--fixture <dir>]`.
  Contact association is always an explicit `--person`/`--org` list —
  nothing here guesses who attended.
- **Real published fixture import.** symaira-meet's own v1 ecosystem
  integration contract
  ([symaira-meet#19](https://github.com/danieljustus/symaira-meet/issues/19))
  landed, publishing the actual meeting-artifact manifest schema and a
  synthetic fixture
  (`Tests/Fixtures/integration/meeting-complete/manifest.json`).
  `meeting import --fixture <dir>` now reads that real
  `manifest.json` (`schema_version`, `meeting_id`, `source`, `created_at`,
  `consent.status`, `retention.policy`) instead of the earlier
  provisional stand-in shape; an unsupported `schema_version` is
  rejected rather than parsed best-effort. `internal/cli/testdata/
  symmeet/meeting-complete/manifest.json` vendors a verbatim copy of the
  published fixture for tests.
- **Source/consent/retention status display.** `meeting import` prints
  the manifest's `source`/`consent.status`/`retention.policy` to Stderr
  as an informational line — satisfying the parent issue's "status
  display" criterion — without ever persisting them; the stored
  interaction still only carries the opaque meeting id (see
  `MeetingImportInput`). Consent/retention enforcement stays SymMeet's
  job, per the parent issue's "leaving ... consent records ... ownership
  with SymMeet."

## What is still blocked upstream

- **Candidate/entity work is unaffected here** — SymMeet's contract only
  covers meeting artifacts, not the Memory-side ID/provenance work
  tracked in [symaira-memory#343](https://github.com/danieljustus/symaira-memory/issues/343)
  and [#344](https://github.com/danieljustus/symaira-memory/issues/344)
  (that's [SYMMEMORY.md](SYMMEMORY.md)'s concern).
- The real `manifest.json` carries no `title`/`summary` field, unlike the
  earlier provisional stand-in shape — `--title`/`--summary` are always
  explicit-flag-only now when importing from a fixture.
- No real `symmeet` binary was available to verify `Discover`'s runtime
  handshake end-to-end (unlike SymMemory — see
  [SYMMEMORY.md](SYMMEMORY.md)); the `capabilities --json` shape it reads
  was cross-checked against symaira-meet's own
  `CapabilitiesCommand.swift` source instead.

The interaction/idempotency layer, `Discover`, and the fixture-manifest
parsing above are all real and tested end-to-end against the published
contract.
