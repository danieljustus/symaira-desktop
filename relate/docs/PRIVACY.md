# Privacy and data-handling policy

`symrelate` is the authoritative local store for direct contact
identifiers — names, email addresses, phone numbers, postal addresses —
and the relationships and interaction history built on top of them. This
document is the policy referenced by the acceptance criteria for backup,
export, erase and redaction; the code in `internal/domain/security` and
`internal/service/security` implements it.

## Key material

Backup and restore need a passphrase to derive an AES-256 encryption key
(via Argon2id). `internal/domain/security.KeyProvider` is the resolution
abstraction; `DefaultKeyProviders` tries, in order:

1. An explicit passphrase (the `--passphrase` CLI flag, or a value handed
   in directly by a test). **Warning:** the `--passphrase` flag value
   appears in shell history and process listings; prefer the interactive
   prompt or SymVault for sensitive passphrases.
2. The `SYMRELATE_BACKUP_PASSPHRASE` environment variable.
3. A local SymVault installation, detected at runtime via a `PATH` lookup
   for a `symvault` binary — never a compile-time dependency (see
   [ARCHITECTURE.md](../ARCHITECTURE.md)'s standalone-first rule). When
   SymVault is not installed, this step is skipped immediately.
4. An interactive terminal prompt (no-echo via `golang.org/x/term`),
   which is the final fallback when stdin is a TTY. On backup create the
   prompt asks for the passphrase twice to catch typos; on restore it
   prompts once.

Steps 1–3 work with zero external tools installed — that is the
documented standalone fallback. The terminal prompt (step 4) requires a
TTY and `golang.org/x/term`; it is skipped in piped/CI environments.

## Redaction

Contact-point values (email, phone, address, URL, handle) must never
appear in:

- Log lines or diagnostic output (`doctor`, `init`).
- Error messages returned by any service or repository method.
- Test fixtures (synthetic-only, see the import fixtures added by the
  vCard/CSV importer).

As a last line of defense, `internal/cli.Run` passes every command error
through `security.Redact` before writing it to stderr, masking email- and
phone-shaped substrings even if a call site forgot to keep them out.

## Backup and restore

`Backup` (`internal/service/security.Service.Backup`) takes a consistent
snapshot via SQLite's `VACUUM INTO` (safe under WAL, unlike copying the
database file directly) and encrypts it with AES-256-GCM, key derived by
Argon2id from the passphrase and a random salt stored in the backup's
header. `Restore` decrypts fully into memory first — GCM authenticates the
ciphertext, so a wrong passphrase or corrupted backup is rejected before
anything touches disk — and only then writes a temp file and renames it
into place, so a failed restore never leaves a partial database at the
target path. Restore always targets a clean profile path; it does not
merge into an existing database.

## Export

`export json` / `export csv` are deliberately data-minimized: identity
fields and contact points only. Free-text notes and audit metadata are
left out by default, since they are the fields most likely to contain
something the user did not intend to hand to whatever consumes the
export.

## Erase

`contact erase` / `org erase` (`Service.EraseContact` /
`EraseOrganization`) hard-delete the person or organization row inside one
transaction. Every table that names the entity — contact points, aliases,
tag and classification links, organization memberships, relationships
(incoming and outgoing), interactions and follow-ups — cascades via
`ON DELETE CASCADE` foreign keys declared in the schema; nothing is
soft-deleted or left orphaned. The beta chose full removal over a
tombstone-in-place design specifically so "erased" means erased, not
"hidden from ordinary queries but still present in the database file."

Before deleting, the service counts what is about to disappear and writes
one `audit_events` row recording the operation, timestamp, local actor and
those counts — never the erased values themselves.

## Audit log

`audit_events` (`audit list`) is an append-only record of every erase,
backup and restore: `operation`, `occurred_at`, `actor` (the local OS
username, never contact data), `entity_type`/`entity_id` (an opaque id,
not a name), and `detail` (counts only). It exists so a user can answer
"what sensitive operation happened, when, and by whom" without the log
itself becoming a second copy of the data it is auditing.

## Data minimization

- Imports (see the vCard/CSV importer) only master fields the user
  explicitly mapped or the source format defines — no enrichment, no
  scraping, no silent creation of extra fields.
- Exports omit notes and audit metadata by default (see above).
- Nothing in this repository sends contact data to another Symaira tool,
  a memory store, or any external service.
