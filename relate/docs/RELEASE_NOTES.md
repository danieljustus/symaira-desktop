# Release Notes - v0.2.1-beta.1

`symrelate` is a local-first, standalone contact and relationship manager. It serves as the authoritative repository for raw contact points (names, emails, phones, addresses) and the relationships built on top of them, operating entirely offline without any external service dependency.

This release makes the quick-add contact path transactional and updates the
SQLite driver dependency. It also includes the expanded automated coverage
that exercises the CLI, console, service, domain, and application lifecycle
surfaces.

## Resolved Issues

This release resolves the following issues:
- [#68](https://github.com/danieljustus/symaira-relate/issues/68): Make quick-add atomic so failed contact-point writes do not leave phantom people
- [#65](https://github.com/danieljustus/symaira-relate/issues/65): Cover the organization-side service surface
- [#66](https://github.com/danieljustus/symaira-relate/issues/66): Cover organization, follow-up, membership, and import console handlers
- [#67](https://github.com/danieljustus/symaira-relate/issues/67): Cover CLI command groups and human-readable formatters

## Changes

- **Atomic quick-add**: `contact add`, MCP `contact_create`, and the web-console quick-add path now create the person and optional contact points in one transaction. Failed writes roll back the entire operation.
- **SQLite update**: The embedded `modernc.org/sqlite` dependency is updated to v1.55.0.
- **Coverage improvements**: Added integration and unit coverage for organization services, console handlers, CLI command groups, formatters, domain validation, SQLite transactions, and application lifecycle behavior.

## Verified Environments

- **OS**: macOS, Linux, Windows
- **Go Version**: 1.26.5+
- **Database Schema Version**: 7 (automatically migrated on start)

## Intentional Exclusions & Known Limitations

- **SymMemory Context Lookup**: Sibling entity creation/relation linking requires upstream `symmemory` changes. Context resolution is currently ID-only, and name searches fallback to best-effort text match.
- **Stable APIs**: All CLI output JSON contracts, database schemes, and MCP schemas are pre-1.0 and subject to modification.
