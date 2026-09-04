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
- **Archive directory**: `<vault>/archive/ingest/` by default (vault-relative,
  so included in vault backups). When configured outside the vault, falls back
  to `~/.local/share/symdesk/archive` (legacy fallback:
  `~/.local/share/symingest/archive`). **PRECIOUS** — back this up.

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
