# PDF adapter checkpoint (RUST-011, not complete)

Current safety correction: [PDF contract v2](pdfops-v2.md). The records below
are historical v1 checkpoints, not the current tool or overwrite contract.
The v2 adapter requires qpdf for all writers, publishes only new validated
destinations and bounds Unix process-group lifetime and captured output.
Full RUST-011, native non-Unix support and independent acceptance remain open.

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
RENDER_RUNTIME_ROOT=<fresh-runtime-directory>` runs 111 paired PDF local-tool cases
and 13 profile/discovery pairs, the Go adapter, facade and press tests, Go IR/HTML
checks, the whole renderer
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
  itself protect aliases. Real-tool diagnostics now prove that same-path,
  symlink and hardlink merge outputs destroy the original input on both sides.
  This safety gate is **FAILED**, not waived by matching the Go behavior.
  Alias rejection/transactional output needs an explicit versioned contract
  decision and source-bound Go coordination before production wiring.

## Historical bounded continuation (v1)

The nine inherited source hashes and three old binary hashes matched the retained
attempt-69 and `pdfops-02` records before adoption. Originals were copied to the
assigned evidence root. The first real command used those older frozen binaries
as a diagnostic only: `pdfops-real-01` exited 1 at qpdf exit 3 on a Poppler output
with an incorrect trailer `/Size`. Its raw failure was not overwritten.

`pdfops-03` freshly built the profile/discovery WIP. All 111 PDF pairs passed;
the 50-ms press deadline case exposed a process-start race (Go had not reached
the tool, Rust had). The harness now checks an already-expired deadline and a
separate readiness-synchronized running cancellation. It does not discard trace
differences or claim that this proves a running deadline's exact timing.

`pdfops-04` passed 111 PDF pairs, 13 press pairs, 23 Go PDF adapter tests, two
facade tests, 12 Go press tests, four renderer tests (zero skipped), six comparator
rejection controls, IR/HTML checks, strict all-target/all-feature Clippy and fmt.
All 264 commands exited zero. The actual Go 1.26.9 and Rust 1.98.0 compiler paths
are resolved before HOME isolation; compiler hashes and the allowlisted build
environment are included in the report. This is library/facade acceptance only,
not shipped Rust CLI/MCP behavior or Typst rendering.

`real_tools.py` is a separate installed-tool diagnostic with 14 paired cases.
It preserves raw argv/stdin/stdout/stderr/exits, PDF semantic objects and text,
input hashes, malformed-input errors and direct/symlink/hardlink alias results.
Synthetic valid PDF inputs are labelled and checked by qpdf before operations;
only nondeterministic PDF trailer `/ID` values are excluded from comparison.
qpdf warnings remain recorded nonzero exits, never clean validation. Every case
runs even after a pair mismatch. The command exits nonzero when parity, input
preservation or clean PDF validation fails; agreement on an unsafe result is
not a passing safety gate. `pdfops-real-02` completed 14/14 matching pairs but
failed safety: all three merge alias kinds changed the input; ordinary Poppler
split/merge outputs produced `/Size` warnings. Poppler was 26.10.0, qpdf 12.4.2.

The separately retained readiness-synchronized `pdfops-descendants-01` diagnostic
used controlled Python tool inputs with a SIGTERM-resistant descendant. Go
remained blocked on its inherited output pipe until the outer three-second
watchdog; Rust returned after direct-child cancellation but left the descendant
alive. The diagnostic killed its owned groups and verified both descendants
absent. Product descendant cleanup and lifecycle equivalence remain **FAILED**;
the outer harness cleanup is not a product fix. PDF page-count/disk-capture
bounds also remain open. None of these findings permits Go removal or cutover.

Independent spec/parity and quality/security review plus parent acceptance are
pending for the current candidate. Earlier approval covers only the old bounded
PDF checkpoint. Full RUST-011 remains open.

## Remaining dependency order

Historical attempt 69 added **then-uncompiled WIP** for the profile registry, picker
facade, capabilities and ten-second Typst discovery cache in `press.rs`, with
Go/Rust probes and 12 declared paired cases under `scripts/rust-port/press/`.
The owning PDF differential invokes these checks without a new Make target or
dependency. No successful execution of those new Go/Rust paths is recorded.
The previous 111-pair receipt and bounded review apply only to the committed PDF
checkpoint, not this WIP. Python syntax and three comparator rejection controls
pass; these are not compiler, discovery or rendering evidence.

At that historical attempt, the reserved slot-A readiness check found no holder, but `dev-external --status`
then failed its internal-space guard (exit 2, below the required 20 GiB reserve).
The real-PDF gate was not launched. No cleanup or guard bypass was performed.
The assigned external `package-handoff.json` and `resume-69/` retain raw guard
output, changed-source hashes, static checks and exact resumption commands.
The scheduler subsequently restored the reserve under separate authorization.
The continuation above supersedes that storage hold and uncompiled-WIP status,
not the failed safety gates or outstanding independent review.

1. Complete PDF safety/real-tool corpus and wire actual Rust CLI/MCP consumers.
2. PDF renderer profiles/frontmatter/assets/Typst compilation/output, including
   unavailable-engine errors, image confinement, overwrite and cancellation.
3. Export Note/View/CSV and archive generation/text-layer behavior.
4. Draw DSL, layout, fonts/text metrics and SVG/PDF/PNG/GIF output contracts.
5. Native target matrix, fuzz/resource gates and independent frozen-candidate
   review. Full RUST-011, RENDER-001/002/003 and Desktop migration remain open.
