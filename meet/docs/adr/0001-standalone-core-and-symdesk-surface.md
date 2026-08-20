# ADR 0001: Standalone core and SymDesk surface

## Decision

Keep `SymMeetCore` self-contained and expose integration points through the
`symmeet` runtime contracts. SymDesk consumes Markdown and JSON output but does
not become a build-time dependency.

## Consequences

Artifacts remain usable without SymDesk or any other Symaira tool. Integrations
must tolerate additive fields and negotiate incompatible major versions rather
than linking to private implementation details.
