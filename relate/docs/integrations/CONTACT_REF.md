# Contact reference contract

`symrelate` publishes a **reference-only contact shape** so external
Symaira tools (first consumer: SymDesk meeting notes) can store an opaque
reference to a Relate contact and resolve it later — without ever
receiving contact points (phone, email, address, URL, handle), notes,
aliases, or any other private field, and without any database access.

This document is the consumer-facing contract for that shape. It
complements [CLI_CONTRACT.md](../CLI_CONTRACT.md) (the general
versioning/transport rules) and [PRIVACY.md](../PRIVACY.md) (the
redaction policy); where the integration docs for
[SymMeet](SYMMEET.md) and [SymMemory](SYMMEMORY.md) describe what
`symrelate` *consumes*, this one describes what it *provides*.

## Resolving a reference

```bash
symrelate contact ref <id> --json
```

`<id>` is the stable ID of a person **or** an organization; the lookup
resolves both. Output is JSON by default; `--human` prints a short
readable line (no compatibility guarantee, per CLI_CONTRACT.md).

The lookup is a pair of primary-key reads of the ID and display-name
columns only. It never loads child tables, so contact points and notes
cannot appear in the output by construction, not by filtering.

## The shape

```json
{
  "provider": "symrelate",
  "schema_version": 1,
  "id": "3f6b8d3c-…",
  "kind": "person",
  "display_name": "Ada Lovelace"
}
```

| Field | Meaning |
|---|---|
| `provider` | Always `"symrelate"`. Consumers key on this to identify the issuing tool. |
| `schema_version` | Contact-reference schema version (integer), currently `1`. Independent of the storage `schema_version` and the wire `api_version`. |
| `id` | Stable, opaque contact ID. Identity is `id` + `kind` — never the display name. |
| `kind` | `"person"` or `"organization"`. |
| `display_name` | Rendering cache so a consumer can display a linked contact without a runtime call. May be refreshed at review time; not an identity field. |

**The shape never contains** contact-point values (raw or normalized),
notes, aliases, tags, classifications, or filesystem paths. A contract
test (`internal/service/contact/reference_test.go`) serializes a fully
populated contact — every contact-point kind, notes, aliases — and
asserts their absence, and pins the exact top-level key set.

## Versioning

Within schema version 1, fields are **additive only** (same rule as
`api_version` v1 in CLI_CONTRACT.md): new optional fields may appear,
existing fields are never renamed, retyped, or removed. Consumers must
preserve unknown fields when round-tripping a reference. Removing or
retyping a field requires a new schema version, announced in the release
notes.

## Errors

- **Unknown or erased ID** — exit code is non-zero and stderr carries a
  generic "contact not found" diagnostic that does not disclose which
  tables were probed or echo private data (PRIVACY.md redaction rules).
  Programmatic callers see `errs.KindNotFound`; the condition is
  machine-distinguishable from transport/unavailability failures.
- **Missing binary, timeout, malformed JSON, wrong `tool` field** — the
  consumer-side discovery failures; treat all of them as "Relate
  unavailable" and degrade gracefully (see below).

## Consumer expectations

Consumers integrating this contract should mirror the pattern
`symrelate` itself uses for SymMemory (see
[SYMMEMORY.md](SYMMEMORY.md)):

1. **Discover at runtime** via a bounded `symrelate version --json`
   handshake on `PATH` (a ~3s timeout works well in practice). Never
   require `symrelate` to build, run, or test the consumer.
2. **Bound every call** with a timeout; a hung or slow `symrelate` must
   not hang the consumer.
3. **Link explicitly and reviewed** — the user chooses which contact a
   reference points at. This contract provides no candidate search or
   automatic identity matching, on purpose.
4. **Degrade gracefully**: Relate absent, reference unresolved (deleted
   or erased contact), or an incompatible `schema_version` all render as
   an unlinked/unknown state, never as an error that breaks the
   consumer's own data.
5. **Store only the reference** — never copy `contact show` output,
   contact points, transcripts, or local paths into the consumer's own
   storage.

## Explicitly out of scope

- Automatic identity matching or candidate suggestion.
- Contact CRUD for consumers; all management stays in `symrelate`.
- Direct database access by consumers (the SQLite file is private to
  `symrelate`; the CLI is the boundary).
