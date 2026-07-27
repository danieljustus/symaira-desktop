# Browser access — implementation proposal

> **Status:** proposal, accepted in principle (see `ARCHITECTURE.md`)
> **Depends on:** #251 (users, groups and document-level permissions)
> **Produces:** document, not code — this issue defines scope, delivery and framework choice so that implementation can start with an agreed boundary.

## 1. Why browser access

SymDesk today serves Mac and iOS users through native SwiftUI apps. In the self-hosted deployment the vault lives on a Raspberry Pi, Mac mini or NAS, and everyone who needs to reach it — Windows, Linux or borrowed machines — has no path in at all. The only alternative is requiring a Mac for every human in the workflow, which does not hold for the household / small-office scenario the self-hosted server was built for.

The decision is already made: browser access exists. This proposal defines what it is, what it is not, and how it fits into the existing architecture without compromising the Markdown-vault contract or the native-client investment.

## 2. What the browser surface does

### Must support

| Task | Why |
|------|-----|
| Browse the vault (folder tree, file list, recently changed) | Primary read path; every user needs it |
| Read Markdown notes (rendered, not source-only) | The whole point of browser access |
| Full-text search over the vault | Parity with native apps; FTS5 already exists in the sidecar |
| View document metadata (frontmatter, properties, backlinks) | Same data the native clients expose |
| Open and preview attachments (PDF, images) | Attachments are part of the vault |
| Read access governed by permissions | Every document visible to the user, and only those documents |
| Log in with individual credentials | Not a single shared token; permissions require identity |

### Stays native-only

| Capability | Reason |
|------------|--------|
| Edit Markdown notes | Phase 1 of the browser UI is read-only; editing from the browser is a separate delivery stage with its own conflict model |
| Write files into the vault (upload, ingest, create) | Write paths require the full permissions model (#251) to be complete and tested; browser writes are a follow-on stage |
| AI chat / RAG over the vault | The AI dock is a desktop feature with local-model integration; exposing it in a browser raises cost, auth and abuse-surface questions that need their own proposal |
| OCR worker management and job queue | Operator/admin workflows stay on the desktop/server CLI |
| Offline access | The browser client is thin; offline is a native-client strength |
| Graph view (force-layout) | Heavy rendering — needs is own scoping; a browser graph is not the same feature as the native one |
| Serve as a mobile client replacement | Native iOS and iPadOS apps exist and stay the primary mobile path; the browser UI targets desktop-class screens and is not designed for phone viewports |

### Explicitly not: an editor

The browser surface is not the same application as the native apps. It provides read access to the vault with rendered Markdown and search. Editing, uploads and AI capabilities stay native-only in the first delivery stage. This boundary keeps the first browser stage small, testable and independently useful: every member of a household or small office can read the shared archive without needing a Mac, and editing follows when the permissions model has been exercised in read-only mode and the write-conflict story is clear.

## 3. Delivery model

### Served by `symdesk serve`

The browser UI is a set of static assets (HTML, CSS, JS) embedded in the `symdesk` binary with Go's `embed` package and served alongside the existing `/api/v1` endpoints. No separate web server, no reverse-proxy requirement beyond what the deployment already runs.

Route structure when browser access is enabled:

```
/healthz                              unauthenticated health check (already exists)
/api/v1/status                        authenticated API (already exists)
/api/v1/snapshot                      authenticated API (already exists)
/api/v1/files                         authenticated API (already exists)
/api/v1/...                           all existing routes untouched
/                                     redirect to /ui/
/ui/                                  browser UI entry point (login page when unauthenticated)
/ui/assets/                           static JS/CSS/images (cached aggressively)
```

The `/ui/` prefix keeps the browser surface cleanly separated from the API namespace. The root redirect makes `http://server:8787` usable as the address to give people.

### Authentication at the UI layer

`/ui/` endpoints serve a login page to unauthenticated visitors and the vault browser to authenticated users. The auth flow uses the same bearer-token mechanism as the native clients:

1. Unauthenticated visitor → login page (minimal, self-contained HTML form)
2. User submits credentials → token returned as a secure httpOnly cookie + visible in the UI for native-client use
3. Subsequent `/ui/` requests carry the cookie; `/api/v1` calls from the browser JS include the token in the `Authorization` header
4. The cookie is scoped to the `/ui/` path so it never leaks into API calls from other clients

This means `/ui/` endpoints need a session-reading middleware that can extract the token from either the cookie or the Authorization header, while the existing `/api/v1` middleware stays unchanged (header-only).

### What an unauthenticated visitor sees

- A login page with a single form: username + password + "Log in" button
- No vault contents, no file listing, no search results, no document metadata
- `/healthz` continues to return `{"ok":true}` with no vault details (unchanged)
- All `/api/v1` endpoints return `401` when no valid token is presented (unchanged)
- No self-registration, no "forgot password", no multi-tenancy UI — this is a single-organisation vault, not a SaaS product

### Versioning

The browser UI is versioned against `/api/v1`. The API contract does not change for the browser; the browser is another client of the same versioned interface the native apps use. If `/api/v2` is introduced later, the browser UI can add support as a second stage without breaking the `/api/v1` deployment.

### Deployed separately (optional)

The embedded-in-binary path is the default and recommended deployment. For operators who want to serve the browser UI from a separate host (CDN, reverse proxy, static site host), the `embed`-based asset serving is optional: `symdesk serve` can run without it, and the UI assets can be built and deployed independently, pointed at the same `/api/v1` backend. This keeps the door open for a dedicated web deployment without requiring it.

## 4. Framework and build choice

### Constraints

1. **CGO-free.** The Go core cross-compiles to linux/darwin/windows × amd64/arm64 without CGO; every dependency must stay within that boundary.
2. **Single binary.** GoReleaser ships one `symdesk` binary; adding a Node.js build step to the release pipeline is a regression.
3. **No SPA framework that needs its own build toolchain.** The abandoned React SPA proved that a separate `web/` directory with its own `package.json`, bundler and CI matrix doubles the maintenance surface for a feature that is read-only and document-focused.
4. **Assets must embed.** Users run `symdesk serve` and get the browser UI without installing extra packages, mounting volumes or configuring paths.

### Decision: server-rendered HTML with minimal progressive enhancement

The browser UI is built from **Go `html/template`** pages served directly by `symdesk serve`, with a small amount of vanilla JavaScript for interactions that genuinely need the client (search-as-you-type, collapsible sections, live property filter). No framework, no bundler, no `node_modules`.

Rationale, compared to the alternatives considered:

| Approach | Embeddable | CGO-free | No separate build | Maturity risk | Verdict |
|----------|-----------|----------|-------------------|---------------|---------|
| **Go templates + vanilla JS** | Yes (`embed`) | Yes | Yes | Low — stdlib | **Chosen** |
| SPA embedded via `embed` (React/Vue/Svelte) | Yes | Yes | No — needs bundler in CI | Low for framework, high for build integration | Rejected: rebuilds the abandoned SPA problem |
| WASM frontend (Go compiled to WASM) | Yes | Yes (tinygo) | No — separate wasm build | Medium — DOM bindings are still thin | Rejected for v1; revisit if client-side logic grows |
| Separate static site deployed beside server | N/A | N/A | Partial | Low | Supported as optional deployment variant |
| HTMX + Go templates | Yes | Yes | Yes (single script tag) | Low | Considered; htmx is a 14 KB dependency that replaces ~80 % of the JS we would otherwise write. Acceptable if the team prefers it over vanilla JS for the AJAX interactions. Either path is fine; the proposal does not pre-decide between vanilla JS and htmx — both satisfy the constraints. |

### What the template layer looks like

```
internal/webfront/           # new package
  server.go                  # HTTP handler, mounts /ui/ routes
  templates/                 # embedded with //go:embed
    base.html                # shell: doctype, head, nav skeleton
    login.html               # login form
    browse.html              # vault browser: folder tree + file list
    note.html                # rendered Markdown note view
    search.html              # search results
    error.html               # error pages (401, 403, 404)
  assets/                    # embedded with //go:embed
    style.css                # single stylesheet, dark/light via prefers-color-scheme
    app.js                   # ~200 lines: search debounce, collapsible sections, fetch wrappers
```

Templates receive data from the same service layer the CLI and MCP server use (`internal/service`). Rendering a note calls the vault reader, applies the permissions filter, converts Markdown to HTML (existing `internal/service` already does this for the API), and injects the result into the template. No new data path, no second source of truth.

### Markdown rendering

The browser UI renders Markdown to HTML server-side using the same Goldmark pipeline that already exists in the service layer. The rendered HTML is sent to the browser inside the template; the browser does not parse or render Markdown itself. This keeps the rendering logic in one place (the Go core) and ensures the browser sees the same output the native Markdown preview produces.

### CSS approach

A single stylesheet, no CSS framework. Dark and light themes are driven by `prefers-color-scheme` with CSS custom properties. The design language matches the native apps (Symaira design tokens where applicable) but the browser UI is not a pixel-for-pixel port — it is a document-focused reading surface, not an app shell.

### Build integration

The `embed` directive makes the templates and assets part of the `symdesk` binary at compile time. `go build ./cmd/symdesk` produces a binary that serves the browser UI. No additional build step. `make build` and GoReleaser stay unchanged.

During development, `symdesk serve` can optionally read templates and assets from a directory on disk (behind a `--dev-ui` flag) so changes are visible on reload without recompiling. Production builds always use the embedded files.

## 5. Permissions interaction

The browser UI depends on the permissions model introduced in #251 (users, groups, document-level read/write rules). The dependency is a prerequisite, not a parallel track: without per-user identity and per-document access control, a browser-reachable archive protected by one shared token would expose every document to anyone who obtains that token — a worse security position than having no browser access at all.

### How the browser uses permissions

- **Login** maps a username + password to a user identity and issues a token scoped to that user.
- **Every data fetch** from the browser to `/api/v1` carries the user's token. The server's auth middleware resolves the user and applies the same read-path filtering the native apps use.
- **File listing and search** return only documents the authenticated user can read (per-user snapshot filtering, already implemented in #251).
- **A note page** refuses to render if the user lacks read permission (403, not a leaked document).
- **Admin functions** (user management, group membership, token rotation) stay CLI-only in the first browser stage. A browser-based admin panel is a separate proposal.

### Migration path

The existing single-token deployment continues to work: the legacy token is treated as an admin identity (already implemented in #251). When browser access is enabled, the login page becomes the primary entry point, but the legacy token still authenticates all `/api/v1` calls. This means operators can enable the browser UI without first migrating every client to per-user credentials.

## 6. Implementation stages

Each stage is independently useful and can ship as a single PR.

### Stage 1: Login page + session cookie

- `internal/webfront/` package with template embedding
- `/ui/` route group on `symdesk serve` (gated behind a `--browser-ui` flag, off by default)
- Login page with username + password form
- Token issued as httpOnly cookie
- Unauthenticated visitors see only the login page
- What ships: an operator can enable the flag and see a login page at `http://server:8787/`

### Stage 2: Vault browser (read-only)

- Folder tree and file list rendered from the same `/api/v1/snapshot` data the iOS app consumes
- Note view: rendered Markdown with frontmatter properties and backlinks
- Search: full-text over the sidecar, results in the same template as file listing
- Attachment preview: PDF and images served through existing `/api/v1/files`
- Every data call filtered by the authenticated user's permissions
- What ships: any family or team member can log in and read the shared archive from any device with a browser

### Stage 3: CSS polish + responsive baseline

- Stylesheet completion (dark/light, typography, spacing)
- Print stylesheet so notes print cleanly
- Minimum usable at 1024 px width; not designed for phones, but readable on tablets

### Later (separate proposals)

- Browser-based editing with conflict detection
- File upload and ingest from the browser
- Admin panel for user/group management
- Browser-based AI chat
- Graph view

## 7. What this replaces and what it does not

### Replaces

- The old decision that there will be no web UI (`PLAN.md` §0, "Keine Web-UI")
- The stale `.gitignore` entries for `web/node_modules/` and `web/dist/` from the abandoned React SPA — these are removed as part of this proposal's implementation

### Does not replace

- The native macOS and iOS apps (editing, AI, offline, graph, OCR management)
- The CLI and MCP server (automation, scripting, agent integration)
- The JSON HTTP API (machine clients, workers, the native apps themselves)
- The Markdown-vault contract (the browser is a client of `/api/v1`, never a second source of truth)

## 8. Open questions

These are decisions the implementing PR should make, not blockers for this proposal:

1. **htmx vs vanilla JS for the AJAX interactions.** Both satisfy the constraints. The implementing PR picks one based on the size and complexity of the search and navigation interactions once the templates are written.
2. **Session token lifetime.** Refresh, expiry and logout behaviour. The simplest starting point: token lives for the browser session (no persistent storage), logout clears the cookie.
3. **Whether to keep `--browser-ui` opt-in forever or make it default-on.** Start opt-in while the feature matures; reassess after Stage 2 ships.
4. **i18n.** The browser UI ships in English initially; the native apps are already localised and the browser can follow that pattern later.

## 9. Revisions to existing documents

As part of implementing this proposal:

- `docs/PLAN.md` §0: remove the "Keine Web-UI" line and replace it with a reference to this document.
- `.gitignore`: remove `web/node_modules/` and `web/dist/` entries (no `web/` directory exists and one will not be reintroduced).
- `AGENTS.md`: update the "no embedded web UI" anti-pattern to reference this proposal and note that a server-rendered HTML surface served by the Go binary is the chosen path.
