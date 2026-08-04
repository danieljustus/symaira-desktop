# CLI-only capabilities: rationale

Issue #307 requires that every core capability is either reachable from the
app or has a written rationale for staying CLI-only. The table below covers
the capabilities that remain deliberately CLI-only after the #307 app-surface
work, with the reasoning per capability.

| Capability | CLI | App surface | Rationale |
|---|---|---|---|
| Retention rules | `symdesk retention eval/list/accept/reject/diff/history` | none | Retention **deletes user files permanently**. The review flow (eval → diff → accept/reject) is deliberately a deliberate, auditable, terminal-driven workflow: the diff output, run history and explicit accept/reject pairing map naturally to a terminal session and are easy to script for scheduled runs. A GUI button would invite one-click permanent deletion of user data without the audit trail the CLI provides. The `history` command keeps a full run log for accountability. |
| Recipes | `symdesk recipe validate/run/<action> <run-id>` | none | Recipes are declarative YAML files authored in a text editor and run against a reviewable proposal. The workflow is file-in, review-diff, accept — the same accept/reject audit loop as retention. Recipes are power-user automation (cron, CI, bulk ops) where the natural home is a shell; the app already surfaces the *result* of recipe-driven changes through its normal file views. |
| Web clipping | `symdesk clip <url>` (symfetch) | none | Clipping fetches remote content and needs interactive consent for network access, cookie/session handling and per-site rules. It is a single-shot command whose output lands directly in the vault's consume path; the app's consume-folder and ingest-queue views already show the results. Building a browser-based clipper into the macOS app would duplicate symfetch and its per-site profile system. |
| Expiring share links | `symdesk server share ...` (internal/selfhost/share.go) | none | Share links are a server-side feature: they expose vault documents over HTTP with expiry and access control. Managing them requires the server operator's mental model (token, expiry, TLS/reverse-proxy setup) and is inherently tied to the `symdesk server` lifecycle, which runs outside the desktop app. The app is a *client* of the server and has no privileged server context. |
| Users / groups / permissions | `symdesk perm user/group/...` | none | Permissions gate who can read what on the **server**. Like share links, they belong to the server operator's surface (multi-user setups, token minting, group membership). The desktop app operates as one authenticated user and must not be able to mint users or tokens — that would be an escalation path. A permissions UI belongs in a server admin surface, not the client app. |

## What the app does surface (complementary)

- **Export** — now in the app: document context menu → Export as PDF/HTML
  (`symdesk export` behind a save panel).
- **Near-duplicates** — now in the app: sidebar → Possible Duplicates lane
  (`symdesk duplicates`), with per-member trash action.
- **Version history / restore** — sidebar → Version History, plus per-note
  context menu → Show Version History (diff + restore).
- **Trash & restore** — sidebar → Trash (restore/purge), plus per-note
  context menu → Move to Trash.
- **Paperless import** — Settings and onboarding (guided import flow).
- **Daily note** — File menu → New Daily Note (Cmd+Shift+D) and the Command
  Palette.

The CLI-only capabilities above are intentionally not duplicated in the app:
each is either destructive-and-auditable (retention, recipes), network- and
profile-bound (clipping), or server-operator-scoped (share links,
permissions). Their *results* remain visible in the app through the normal
vault views, ingest queue and document grid.
