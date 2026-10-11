# PDF adapter checkpoint (RUST-011, not complete)

The Rust `symdesk_render::pdfops` library provides split, merge and rotate
through Poppler/qpdf, plus the ingest facade's lexical absolute-path behavior.
It is not yet connected to the Rust CLI/MCP. The Go production paths are intact.

## Executed evidence

The live test-only Go driver calls the actual Go adapter and `internal/ingest`
facade. Its `fixtureoracle.Current()` validates all Go production inputs against
the central portgen source identity. The Python gate freezes all tracked and
untracked build/harness inputs before execution, checks the actual Cargo root,
pins Go 1.26.9/Rust 1.98.0, hashes the built executables and checks input identity
again afterward. It never generates expected Go responses by hand or overwrites
an older capture.

`make render-differential RENDER_EVIDENCE_ROOT=<fresh-evidence-directory>
RENDER_RUNTIME_ROOT=<fresh-runtime-directory>` runs 111 paired local-tool cases,
the Go adapter and facade tests, existing Go IR/HTML checks, the whole renderer
nextest suite, strict all-target Clippy and workspace fmt. Optional
`RENDER_BUILD_LAUNCHER` selects the required local storage launcher; the caller
owns the build-slot grant. The target preserves `make`'s original default build.

Each side has a fresh HOME/XDG/temp/cwd, an allowlisted environment, and a PATH
containing only the freshly built controlled tool stand-ins. Captures include
raw driver stdout/stderr, complete tool argv, response/error semantics, input
hashes, output bytes/modes and directory deltas. Cancellation waits for a real
tool-ready marker and verifies direct-child death. Only isolated root paths and
the Go split workspace's random basename are normalized. JSON comparisons are
strict about types and missing fields. Failed cases and original logs survive.
This is process-adapter evidence, not valid-PDF, sandbox or native-matrix proof.

Local Darwin/arm64 diagnostic `pdfops-02` passed all 111 pairs, 23 Go adapter
tests, 2 Go facade tests, 3 Rust tests, 6 comparator mutation controls, Go IR/HTML,
Clippy and fmt. The earlier `pdfops-01` failures are retained: existing-file
MkdirAll wording and lexical parent-directory cleaning were corrected. Raw
receipts are in the task's assigned external evidence root, not this repository.

## Explicit differences and pending review

- `RENDER-PDF-LIMIT-1`: selectors expanding beyond 1,000,000 pages fail closed.
  Go can allocate without a bound or wrap its MaxInt loop. This safety limit is
  separately tested, not counted as exact Go parity. Tool capture reads are
  capped at 16 MiB; disk capture and page-count limits need further hardening.
- The process adapter kills/reaps the direct child like Go. Neither this capture
  nor the API claims descendant-tree cleanup. File-backed capture avoids an
  inherited pipe EOF wait, but this is not equivalent to Go's unbounded pipe
  behavior with forking tools. Keep the broader lifecycle gate open.
- Filesystem error text is currently verified on Darwin only. Windows native
  runtime, path lookup and error semantics remain acceptance requirements.
- Callers must supply distinct input/output paths. The Go adapter does not
  itself protect aliases; alias preservation needs a dedicated safety decision
  and real-tool proof before production wiring.

## Remaining dependency order

1. Complete PDF safety/real-tool corpus and wire actual Rust CLI/MCP consumers.
2. PDF renderer profiles/frontmatter/assets/Typst compilation/output, including
   unavailable-engine errors, image confinement, overwrite and cancellation.
3. Export Note/View/CSV and archive generation/text-layer behavior.
4. Draw DSL, layout, fonts/text metrics and SVG/PDF/PNG/GIF output contracts.
5. Native target matrix, fuzz/resource gates and independent frozen-candidate
   review. Full RUST-011, RENDER-001/002/003 and Desktop migration remain open.
