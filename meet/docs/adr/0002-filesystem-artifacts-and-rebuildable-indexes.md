# ADR 0002: Filesystem artifacts and rebuildable indexes

## Decision

Use portable files as the meeting source of truth. Any future search index is a
derived cache that can be rebuilt from `manifest.json`, JSONL events, audio, and
transcript artifacts.

## Consequences

The initial milestone does not introduce SQLite or a cloud service. Storage
code must write atomically and validate all caller-provided identifiers and
relative paths.
