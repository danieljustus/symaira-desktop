# Architecture decisions

## Obsidian Canvas and whiteboards

SymDesk deliberately does not render or edit Obsidian `.canvas` files. They
remain ordinary files in the vault and are never modified by the Markdown
index, editor, or document workflow.

Canvas is a separate JSON graph format with an interaction model that does not
fit the application's plain-Markdown source-of-truth contract. A partial
read-only renderer would still need to define attachment, embedded-note, and
layout compatibility, while a full editor would duplicate a specialised
whiteboard application. Keeping Canvas delegated to Obsidian preserves the
file unchanged and makes this boundary explicit instead of implying parity
that does not exist.

If Canvas support becomes necessary, it should begin as a separate proposal
covering read-only rendering, embedded-note resolution, offline assets, and
the link/index contract before implementation starts.

## iOS companion and vault access

The iPhone/iPad app does not embed or remotely invoke the `symdesk` executable.
iOS cannot launch the local CLI process, and requiring a running Mac would
violate the product's local-first and offline goals. Instead, the user grants
the app access to an existing vault folder through the system Files picker.
That security-scoped permission is persisted as a bookmark, and iCloud Drive
or the selected file provider remains responsible for synchronization.

The mobile scanner reads current contract-v6 Markdown (including older
backward-compatible shapes) directly, ignores hidden
directories, coordinates reads with the file provider, and caches parsed notes
by modification date and size. Search therefore stays on-device. Document
previews resolve only files inside the granted vault, preferring contract
metadata (`archive_path`, `source_path`, `original_path`) and then Markdown
attachment links. Quick Look owns format rendering on iOS.

Mobile writes are not ad-hoc: every capture feature (composer, scanner, Share
Extension, inline AI) funnels through one durable outbox that survives app
termination and applies entries with exponential backoff when connectivity
returns. Two adapters behind one protocol implement the same semantics — the
server adapter writes through `PUT /api/v1/files` and `POST /api/v1/ingest`
with a content-hash precondition; the Files/iCloud adapter writes through
`NSFileCoordinator` with a modification-date/size precondition. iCloud Drive
has no atomic compare-and-swap, so the Files-mode precondition is best-effort
and the conflict path is the normal case, not the exception: when a write
would overwrite a remote change, the losing version is preserved as a sibling
"conflicted copy" file (the same convention the desktop `conflict resolve`
command understands) and surfaced in the app's failed-write list. Nothing is
silently discarded.

## Self-hosted server and distributed processing

Self-hosting is a second deployment mode, not a replacement for local-first
vault access. `symdesk serve` owns one single-user vault, archived originals, a
rebuildable server-side index and a durable file-backed processing queue. Mac
and iOS clients authenticate with one bearer token over the versioned HTTP API.
The token is stored in Apple Keychain.

Workers never mount the vault. They lease a job, download its original through
the authenticated API, run Tesseract or a local Ollama vision model, and submit
text plus engine/model provenance. This allows a Raspberry Pi or Mac mini to
remain the always-on storage host while a transient MacBook supplies compute.

The HTTP compatibility endpoint executes only an explicit set of existing CLI
commands and forces the server vault path. Commands that can start services,
access server-local arbitrary paths, export outside the vault or ingest a path
from the server host are excluded. File endpoints reuse canonical vault path
checks and apply independent request/output size limits.
