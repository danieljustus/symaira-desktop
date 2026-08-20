# Threat model

## Assets

- Original audio, microphone and system-audio tracks.
- Raw and edited transcript segments, Markdown projections, and lifecycle
  events.
- Recording authorization records, retention state, and local model artifacts.

## Trust boundaries

- A user-facing interactive authorizer is separate from capture code.
- Meeting artifact paths are untrusted inputs until validated under the
  configured data root.
- Models and imported media are untrusted files; SymDesk and other Symaira
  tools interact only through runtime contracts.

## Abuse cases and mitigations

| Threat | Mitigation |
| --- | --- |
| Malicious media | Treat imports as opaque artifacts until a future decoder validates them; never infer decoders from extensions alone. |
| Path traversal or symlink escape | Validate IDs/relative paths, reject symlinked components, and keep every store operation under its data root. |
| Poisoned model artifact | Keep models local and separately cacheable; verify provenance and integrity before a future model loader executes them. |
| Stale consent token | Mint only from an interactive authorizer; bind records to one session, enforce expiry, consume on use, and drop all records on restart. |
| Accidental screen capture | No automatic recording start; capture integrations must use the authorization API and disclose scope. |
| Log leakage | Emit only structured status/opaque IDs and filter content, people, calendar, credentials, and absolute paths. |
| Cross-process race | Use actor-owned state and atomic sibling-file writes with rename; a future capture agent must not share mutable authorization state. |

## Accepted initial-release risks

This milestone does not ship encryption at rest, a media decoder sandbox,
cryptographic model verification, or a capture UI. Those omissions are visible
product limitations, not implied protections. Users remain responsible for
obtaining legally appropriate notice and consent for their context.
