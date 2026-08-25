# SymDraw Mermaid Subset — Contract (DialectVersion 1.0)

This document is the contract for the supported Mermaid dialect in SymDesk's
deterministic diagram pipeline (`internal/draw/parse`). The parser implements
exactly this subset; anything outside it fails with a typed `ParseError`
naming the construct and an actionable hint — never a silent wrong rendering.

## Supported

### Diagram kinds

| Kind | Status |
|---|---|
| `flowchart` / `graph` (`TD`, `TB`, `BT`, `LR`, `RL`) | ✅ |
| `sequenceDiagram`, `pie`, `classDiagram`, `stateDiagram`, `erDiagram`, `gantt`, `gitgraph`, `journey`, `mindmap`, `quadrantChart`, `xychart`, `requirementDiagram`, C4, `architecture`, `packet`, `kanban`, `block`, `sankey` | ❌ typed error at the header line |

### Flowchart statements

- Node definitions: `A`, `A["label"]`, `A("label")`, `A{"label"}`, `A[["label"]]`,
  `A(["label"])`, `A[("label")]`, `A>label]`, `A[/label/]`, `A[\label\]`,
  `A((label))`, `A(((label)))`, `A{{label}}`, `A==label==`
- Edges: `-->`, `---`, `-.->`, `==>`, `-- text -->`, `-->|text|`, `-. text .->`,
  `== text ==>`, `--x`, `--o`
- Subgraphs: `subgraph Name` … `end` (nested supported)
- `title: ...` / `title ...`
- Notes attached to nodes (`A --> B` with note definitions as in SymDraw IR)

### JSON input path

`ParseJSON` accepts the SymDraw IR schema (see `internal/draw/ir/schema.go`)
with strict decoding: unknown fields, malformed JSON, empty input, and
semantic contract violations all produce typed `ParseError`s
(stages `schema` / `parse` / `contract`).

## Explicitly unsupported (typed errors, always)

| Construct | Example |
|---|---|
| `click` | `click A "https://example.com"` |
| `style` / `classDef` / `class` | `style A fill:#f9f` |
| `linkStyle` | `linkStyle 0 stroke:#ff3` |
| Accessibility directives | `accTitle: ...`, `accDescr: ...`, `accDescr { ... }` |
| `init` directives | `%%{init: {"theme":"dark"}}%%` |
| `%%{...}%%` JSON directives | same |
| `direction` inside a subgraph | `direction LR` |
| Invisible links | `A ~~~ B` |
| `interpolate` | — |

Ordering is deliberate: the parser rejects unsupported constructs before
interpreting anything else on the line, and comments (`%% ...`) are stripped
before parsing (an `init` directive is *not* treated as a comment).

## Versioning

`DialectVersion` (`internal/draw/parse/parse.go`) is the version of this
contract. Bumping it requires: updating this document, pinning new behaviour
in `mermaid_test.go`, and re-verifying the cases pinned by the Swift preview
tests behave identically.