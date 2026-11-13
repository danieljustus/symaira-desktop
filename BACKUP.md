# Backup and restore

The Markdown vault is the source of truth. Back it up together with every
original-document archive managed by companion tools before relying on a
restore point.

## Keep

- The entire vault directory, including Markdown notes, `assets/`,
  `attachments/`, and any originals deliberately stored inside it.
- The symingest original-document archive and its queue, using the paths
  configured in that tool. These locations are companion-tool configuration,
  so verify them with the symingest installation before making a backup.
- The symmemory database if its retained memories are needed after recovery.
- Any external configuration required to recreate companion-tool paths.

Secrets are not vault data. Keep recovery material in its intended secret
store, such as the macOS Keychain or symvault; never copy secret values into a
vault backup or this repository.

## Rebuildable data

- The SymDesk SQLite sidecar is an index and can be rebuilt from the vault with
  `symdesk index`.
- `.symdesk/views.json` is part of the vault and should be retained; it stores
  saved views rather than a disposable index.
- Native-app build products, caches, and generated project files are not user
  data and do not belong in a vault backup.

## Automatic backup before the first Rust run

Starting with the Rust-only release there is no Go binary to fall back to, so
the first Rust run protects what the Go builds left behind. When `symdesk` (or
`symroom`) starts, finds existing state and has not done so before, it copies
that state **once** into

```text
$XDG_DATA_HOME/symdesk/backups/pre-rust/     (default ~/.local/share/symdesk/backups/pre-rust)
$XDG_DATA_HOME/symroom/backups/pre-rust/     (the identity store, see below)
```

and prints a single notice on stderr (never on stdout, so `symdesk mcp` frames
stay clean). The backup is built in a temporary directory and renamed into
place; `manifest.json` is written last and is the completion marker. Later runs
do nothing.

What is backed up:

- the `symdesk` data directory (`config paths` `data_dir`: per-vault sidecar and
  retrieval databases, `symingest.db`, `symrelate.db`, `vaults/<hash>/metadata.json`),
  the config directory (`config.toml`) and the legacy fallback directories of the
  absorbed tools (`symingest`, `symrelate`, `symaira-seek`, `SYMRELATE_DATA_HOME`),
- for `symroom`: the identity store `$XDG_DATA_HOME/symroom/identities` (the
  Ed25519 private keys that sign room journals).

What is not: the Markdown vault (it is yours and is never rewritten by the
backup), room directories, the ingest `archive/` and earlier `backups/`.

SQLite files are copied byte for byte when quiescent; a database with a live WAL
or one that changes while it is copied is snapshotted with SQLite's own
`VACUUM INTO` (WAL-consistent) instead. `manifest.json` records
`{schema_version, created_at, symdesk_version, sources, dirs, files:[{source,
backup, sha256, size, mode, mtime_ns, method}], skipped}`.

If state exists and the backup cannot be completed (read-only disk, no space),
the command is **aborted with a clear error and nothing is changed**. Set
`SYMDESK_PRE_RUST_BACKUP=off` to continue without a backup (the same variable
disables the backup on purpose, for example in hermetic tests). A fresh install
has nothing to back up; a marker records that so state created by Rust itself is
never mistaken for Go-era state.

### Restoring

```sh
symdesk backup restore-pre-rust --dry-run   # verify the backup, show what would change
symdesk backup restore-pre-rust             # restore it
symroom backup restore-pre-rust             # the same for the identity store
```

The command is intentionally absent from `--help` and shell completion. It first
verifies every file against `manifest.json` (nothing is touched if any digest
differs), then moves the current state of each backed-up directory aside to
`backups/pre-restore-<UTC time>-<pid>/` (it never deletes it), places the saved
files back one by one through temporary files and renames, restores modes and
modification times, and re-verifies every restored file. If a step fails the
previous state is moved back. The vault is not part of the restore; run
`symdesk index` afterwards if you also changed notes.

## Absorbed store paths

SymDesk consolidates all absorbed store state under a single XDG application
directory (`$XDG_DATA_HOME/symdesk`, defaulting to `~/.local/share/symdesk`).
Existing installs with state in legacy per-tool directories continue to work
via read-only fallbacks.

To enumerate the active paths for backup scripts, pass the vault explicitly (or
configure it through `SYMDESK_VAULT`) and run:

```sh
symdesk --vault /path/to/vault config paths --json
```

Output:

```json
{
  "data_dir": "/Users/username/.local/share/symdesk",
  "config_dir": "/Users/username/.config/symdesk",
  "cache_dir": "/Users/username/.cache/symdesk",
  "sidecar": "/Users/username/.local/share/symdesk/vaults/<hash>/sidecar.db",
  "retrieval": "/Users/username/.local/share/symdesk/vaults/<hash>/retrieval.db",
  "ingest": "/Users/username/.local/share/symdesk/symingest.db",
  "ingest_archive": "/path/to/vault/archive/ingest",
  "contacts": "/Users/username/.local/share/symdesk/symrelate.db"
}
```

The resolved paths and their classification:

- **Ingest store** (`ingest`): `~/.local/share/symdesk/symingest.db` (legacy
  fallback: `~/.local/share/symingest/symingest.db`). Tracks dedup hashes, job
  history, and original-to-vault mappings. **PRECIOUS** — back this up.
- **Contacts store** (`contacts`): `~/.local/share/symdesk/symrelate.db` (legacy
  fallback: `~/.local/share/symrelate/symrelate.db` or `SYMRELATE_DATA_HOME`).
  Stores contact relationships and entities. **PRECIOUS** — back this up.
- **Sidecar index** (`sidecar`):
  `~/.local/share/symdesk/vaults/<hash>/sidecar.db` (or `symdesk/sidecar.db`
  without a vault). Powers search, backlinks, and metadata. **REBUILDABLE**
  via `symdesk index`.
- **Retrieval index** (`retrieval`):
  `~/.local/share/symdesk/vaults/<hash>/retrieval.db` (or
  `symdesk/retrieval.db` without a vault; legacy fallback:
  `~/.local/share/symaira-seek/symseek.db`). Vector and BM25 index.
  **REBUILDABLE**.
- **Archive directory** (`ingest_archive`): `<vault>/archive/ingest/` by default (vault-relative,
  so included in vault backups). When configured outside the vault, falls back
  to `~/.local/share/symdesk/archive` (legacy fallback:
  `~/.local/share/symingest/archive`). **PRECIOUS** — back this up.

`config paths` reports configured sidecar, retrieval, ingest database, ingest
archive, and contacts overrides as the effective paths. To snapshot the active
retrieval SQLite database consistently, including committed WAL contents, run:

```sh
symdesk --vault /path/to/vault index maintenance backup --output retrieval-backup.db
```

Vault-scoped `relocate` is intentionally rejected because `index_path` is a
global override and would make multiple vaults share one retrieval database.

## Restore walkthrough

1. Install SymDesk on the replacement machine.
2. Restore the vault to its intended location, including any vault-relative
   `archive/` originals. If using a shared external archive, restore it to its
   configured location.
3. Restore the precious databases (`ingest` and `contacts` paths reported by
   `symdesk config paths --json`).
4. Point `SYMDESK_VAULT` at the restored vault and run `symdesk index` to rebuild
   the sidecar index.
5. Run `symdesk doctor`, open a saved view, and search for a known document to
   verify that the rebuilt sidecar and vault contents agree.
6. Restore the symmemory database only when its retained memory is required;
   it is separate from the vault and does not affect Markdown-vault recovery.
The active paths can be verified at any time using `symdesk config paths --json`
or `symdesk doctor`.
