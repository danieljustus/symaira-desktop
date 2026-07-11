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
