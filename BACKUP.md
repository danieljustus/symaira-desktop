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

## Restore walkthrough

1. Install SymDesk and the required companion tools on the replacement machine.
2. Restore the vault to its intended location, then restore the symingest
   original archive and queue to the configured companion-tool locations.
3. Point `SYMDESK_VAULT` at the restored vault and run `symdesk index`.
4. Run `symdesk doctor`, open a saved view, and search for a known document to
   verify that the rebuilt sidecar and vault contents agree.
5. Restore the symmemory database only when its retained memory is required;
   it is separate from the vault and does not affect Markdown-vault recovery.

The current desktop configuration does not expose symingest's archive path or
the user's backup exclusions, so `symdesk doctor` cannot yet verify that
relationship safely. Once symingest publishes a stable archive-path contract,
the doctor check can validate it without guessing.
