# Backup and Cutover Guide

This document describes what to back up, what can be rebuilt, and how to restore a Symaira/SymDesk environment. It is the single source of truth for disaster recovery.

## TL;DR

- **Precious (must be in backups):** Markdown vault, symingest original archive, symingest queue, symmemory database, secrets in Keychain/symvault.
- **Rebuildable:** Desk sidecar index (`symdesk index`), symingest dedup database, derived thumbnails/preview caches.
- **Where to keep backups:** iCloud, Time Machine, or another off-device destination that includes the vault directory and the archive directory.

## Components

### 1. Markdown vault — PRECIOUS

The vault directory contains the Markdown notes with YAML frontmatter. This is the single source of truth for documents, metadata, and knowledge.

- **Default path:** `~/Notes` (or the path configured in SymDesk / `SYMDESK_VAULT`).
- **Backup:** Include the entire vault directory in your backup.
- **Restore:** Copy the vault directory back to the same or a new machine.

### 2. Desk sidecar index — REBUILDABLE

The sidecar index powers search, backlinks, and views. It is derived from the vault.

- **Default path:** `~/Library/Application Support/SymDesk/sidecar`.
- **Backup:** Not strictly required.
- **Restore:** After restoring the vault, run `symdesk index` to rebuild the sidecar.

### 3. symingest original archive — PRECIOUS

symingest keeps the original PDFs/images/EMLs it consumed. These are the raw sources that Re-OCR and audit rely on.

- **Default path:** `~/.local/share/symingest/archive` (or `SYMINGEST_ARCHIVE_PATH`).
- **Backup:** Must be included in your backup.
- **Restore:** Copy the archive directory back to the same path. If the path changes, update `SYMINGEST_ARCHIVE_PATH` or the config file.
- **Important:** The archive should live **inside** the vault directory if possible, so one backup covers both. If the archive is outside the vault, your backup must include both paths.

### 4. symingest database and queue — PRECIOUS

The database tracks dedup hashes, job history, and archive-to-vault mappings. The queue holds pending ingestion jobs.

- **Default paths:** `~/.local/share/symingest/symingest.db`, `~/.local/share/symingest/queue`.
- **Backup:** Include the database and queue directory. If you lose them, you can re-run ingestion, but dedup history and job state are lost.
- **Restore:** Copy back to the same path.

### 5. symmemory database — PRECIOUS

symmemory stores entities, memories, and relationships. It is not derivable from the vault.

- **Default path:** `~/.local/share/symmemory/symmemory.db` (or `SYMMEMORY_DB_PATH`).
- **Backup:** Include in backups.
- **Restore:** Copy back to the same path.

### 6. Secrets — PRECIOUS, but NOT in the vault

Passwords, API tokens, IMAP credentials, and encryption keys live in:

- **macOS Keychain**
- **symvault** (which also uses the Keychain)

These are **not** inside the vault. Back them up via your usual Keychain/Secrets backup strategy.

## Verified restore walkthrough

1. Install Symaira tools on the new machine (Homebrew tap `danieljustus/tap`).
2. Restore the vault directory to the desired path.
3. Restore the symingest archive, database, and queue to their paths.
4. Restore the symmemory database.
5. Restore Keychain/symvault secrets.
6. Open SymDesk and point it to the vault.
7. Run `symdesk index` to rebuild the sidecar.
8. Run `symdesk doctor` to verify tool availability and archive/vault coverage.
9. Open a note and verify that the original PDF opens and Re-OCR is available.

## symdesk doctor

`symdesk doctor` checks whether the symingest archive lives inside the vault backup path. If it does not, the doctor report warns you that your backup strategy must cover two separate locations.

## See also

- `README.md` for the project overview.
- `docs/RULES-SETTINGS.md` for rules and mail configuration.
- `symingest` docs for ingestion-specific paths and options.
- `symmemory` docs for memory database setup.
