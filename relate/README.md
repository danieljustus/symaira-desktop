# Symaira Relate

> **Dieses Verzeichnis war bis 2026-08-23 das eigenständige Repository
> [`symaira-relate`](https://github.com/danieljustus/symaira-relate).** Die Go-Implementierung
> ist nun direkt in das Root-Modul von [`symaira-desktop`](../README.md) unter `internal/contacts`
> aufgelöst (Issue #538 Migration). Die Kontakt- und Beziehungsverwaltung wird über
> `symdesk contact` genutzt.
> Die Badges, Installations- und Release-Hinweise unten stammen aus der
> Zeit als eigenes Repo und gelten nicht mehr.

*Local-first contact and relationship manager — CLI, web console and MCP server over one encrypted-at-rest-aware SQLite vault on your machine.*

[![CI](https://github.com/danieljustus/symaira-relate/actions/workflows/ci.yml/badge.svg)](https://github.com/danieljustus/symaira-relate/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/danieljustus/symaira-relate)](https://goreportcard.com/report/github.com/danieljustus/symaira-relate)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

![Symaira Relate social preview](docs/assets/social-preview.png)

`symrelate` is a standalone, local-first contact and relationship manager.
It stores people, organizations, relationships, interaction history and
follow-ups in a single SQLite database on your machine — no account, no
cloud service, no network calls.

## Why Symaira Relate

- **Local-first by design** — your contacts live in one SQLite file on your
  machine; there is no cloud account to sign up for and nothing leaves the
  device unless you back it up yourself.
- **Privacy by default** — contact points never appear in logs, errors or
  diagnostics, backups are AES-256-GCM encrypted, and erasing a contact is a
  hard delete with an audit trail.
- **Agent-ready** — a deliberately narrow MCP server over stdio exposes the
  safe read/write surface to AI clients without widening the attack area.
- **Stable JSON contract** — every command prints versioned, snake_case JSON
  (`api_version: v1`) so scripts and agents can rely on the output shape.
- **Standalone-first** — builds and runs with zero other Symaira tools;
  optional integrations are detected at runtime and degrade gracefully.

```console
$ symrelate contact add --display-name "Ada Lovelace" --email ada@example.com
{"id":"01J...","display_name":"Ada Lovelace",...}
```

## Status

Beta (`v0.2.1-beta.1`). The CLI surface and on-disk schema are pre-1.0 and
may still change between beta releases.

## Install

```sh
go install github.com/danieljustus/symaira-relate/cmd/symrelate@latest
```

Prefer `go install`; prebuilt Linux/macOS/Windows binaries are attached to
each [release](https://github.com/danieljustus/symaira-relate/releases).

Requires Go 1.26.5+. `symrelate` builds with `CGO_ENABLED=0` and has no
compile-time dependency on any other Symaira tool — it runs standalone with
no other Symaira binary installed. See [ARCHITECTURE.md](ARCHITECTURE.md)
for the standalone-first and data-ownership boundaries this implies.

## Quick start

```sh
symrelate doctor          # verify paths and database health
symrelate version --json  # {"tool":"symrelate","version":"...","schema_version":N,"api_version":"v1"}
```

Every data command prints stable, versioned JSON by default; add
`--human` for a short readable summary instead (e.g. `symrelate contact
show --human <id>`). See [docs/CLI_CONTRACT.md](docs/CLI_CONTRACT.md) for
the full compatibility contract.

Agents can talk to `symrelate` directly via `symrelate mcp`, a narrow MCP
server over stdio — see [docs/MCP.md](docs/MCP.md).

For everyday contact management without the CLI, run `symrelate console`
for a localhost-only, authenticated web UI — see
[docs/CONSOLE.md](docs/CONSOLE.md).

## Data location

Paths follow the XDG Base Directory convention:

| Purpose | Default | Override |
|---|---|---|
| Config | `$XDG_CONFIG_HOME/symrelate` (`~/.config/symrelate`) | `SYMRELATE_CONFIG_HOME` |
| Data (the SQLite database) | `$XDG_DATA_HOME/symrelate` (`~/.local/share/symrelate`) | `SYMRELATE_DATA_HOME` |
| Cache | `$XDG_CACHE_HOME/symrelate` (`~/.cache/symrelate`) | `SYMRELATE_CACHE_HOME` |

## Importing existing contacts

```sh
symrelate import vcard --dry-run testdata/import/sample.vcf   # preview, writes nothing
symrelate import vcard testdata/import/sample.vcf             # apply
symrelate import csv --map name=Full Name,email=Email sample.csv --dry-run
symrelate import runs                                         # history of past imports
```

vCard (3.0/4.0) and CSV import share one workflow: a dry run never writes,
duplicate candidates (matched on normalized contact points, names, or a
prior import of the same source) require an explicit `--resolve
ROW=create|skip|merge:PERSONID`, and re-importing an unchanged source is
idempotent — nothing is duplicated on a second run.

## Data protection

Sensitive contact-point values never appear in logs, errors or `doctor`
output; backups are AES-256-GCM encrypted; erasing a contact is a hard
delete with an audit trail. See [docs/PRIVACY.md](docs/PRIVACY.md) for the
full policy.

## Optional integrations

`symrelate` never requires another Symaira tool to build, run, or pass
its tests (see [ARCHITECTURE.md](ARCHITECTURE.md)'s standalone-first
rule). Two integrations are detected at runtime and degrade gracefully
when absent:

- `symrelate memory status|link|unlink|show` — an optional link from a
  contact to a SymMemory entity for reviewed context lookup. See
  [docs/integrations/SYMMEMORY.md](docs/integrations/SYMMEMORY.md).
- `symrelate meeting status|import` — import a SymMeet meeting as a
  reviewed, idempotent contact interaction. See
  [docs/integrations/SYMMEET.md](docs/integrations/SYMMEET.md).

Both documents state plainly what's implemented versus still blocked on
open issues in their respective sibling repositories.

## Known limitations (beta)

- The CLI JSON contract (`api_version`), on-disk schema, and MCP tool
  catalogue are pre-1.0 and may still change between beta releases.
- SymMemory: candidate matching and writing relations into SymMemory are
  now implemented via the upstream `entity resolve` and `entity relate`
  commands; see [docs/integrations/SYMMEMORY.md](docs/integrations/SYMMEMORY.md).
  `memory show` still shells out to `entity show`, which resolves by name
  rather than id, so context lookup may report `context_available: false`
  for id-only links until SymMemory adds id-based entity show.
- SymMeet: `meeting import --fixture` reads the real published meeting
  manifest format now that symaira-meet#19 landed; no real `symmeet`
  binary was available to verify the runtime discovery handshake
  end-to-end. See [docs/integrations/SYMMEET.md](docs/integrations/SYMMEET.md).
- The web console and MCP server intentionally expose a narrower surface
  than the CLI (no backup/restore, export, or audit-log viewing) — see
  [docs/CONSOLE.md](docs/CONSOLE.md) and [docs/MCP.md](docs/MCP.md).

See [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) for common issues
and [docs/BETA_MATRIX.md](docs/BETA_MATRIX.md) for the manual QA checklist
run before each beta release. [CHANGELOG.md](CHANGELOG.md) tracks what's
landed.

## Architecture

```mermaid
graph TB
    subgraph "User Interface"
        CLI[CLI symrelate]
        Console[Web Console]
        MCP[MCP Server]
    end

    subgraph "Core"
        Contact[Contact Manager]
        Relationship[Relationship Engine]
        Import[Import Pipeline]
        Export[Export Engine]
    end

    subgraph "Storage"
        SQLite[(SQLite Database)]
        Backup[AES-256-GCM Backups]
    end

    subgraph "Optional Integrations"
        SymMemory[SymMemory]
        SymMeet[SymMeet]
    end

    CLI --> Contact
    CLI --> Relationship
    CLI --> Import
    CLI --> Export
    Console --> Contact
    MCP --> Contact

    Contact --> SQLite
    Relationship --> SQLite
    Import --> SQLite
    Export --> SQLite

    Contact --> Backup
    SQLite --> Backup

    Contact -.-> SymMemory
    Import -.-> SymMeet

    style CLI fill:#4CAF50,color:white
    style Console fill:#2196F3,color:white
    style MCP fill:#9C27B0,color:white
    style SQLite fill:#FF9800,color:white
    style Backup fill:#F44336,color:white
```

Key principles:
- **Standalone-first**: No other Symaira tool required to build, run, or test
- **Local-first**: All data stored in SQLite on user's machine
- **Privacy by design**: Sensitive values never leave the local machine
- **Optional integrations**: SymMemory and SymMeet detected at runtime, degrade gracefully

See [ARCHITECTURE.md](ARCHITECTURE.md) for detailed design decisions.

## Development

```sh
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```
