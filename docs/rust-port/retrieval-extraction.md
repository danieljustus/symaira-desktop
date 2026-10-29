# Retrieval document extraction: bounded parity slice

The Rust re-embed route now dispatches HTML/HTM, RTF, DOCX, XLSX,
PPTX, ODT/ODS/ODP, EPUB and PDF. Markdown, plain text and CSV keep their
existing implementations. This is not unrestricted format compatibility or
native-platform migration acceptance.

## Executable evidence

`internal/retrieval/internal/parser/port_extract*_test.go` synthesizes all input
bytes deterministically, including ZIP members and PDF object/xref tables.
`TestRetrievalExtractionFixture` calls both production `ParseFile` and
`ParseFileSections`, recording exact text bytes, anchors, offsets, synthetic
flags and error messages. Check mode recomputes and compares without writing.
The corpus has 60 cases, including malformed XML/ZIP/PDF, HTML5 repair/entities,
RTF control/destination quirks, missing archive parts, sheet/slide/chapter order,
EPUB encryption metadata, input/entry truncation, archive entry count and the
100-MiB decompression budget. It is a synthetic corpus, not real customer data.

The engine re-embed oracle reuses only those generated **input** bytes and
independently captures embedding requests and SQLite chunks/state. The existing
real Rust CLI HTTP differential now executes 22 cases, adding ten cases for
HTML, RTF, DOCX, XLSX, PPTX, ODT, ODS, ODP, EPUB and PDF. The provider is an
isolated loopback fake, not a paid service. Test children have isolated homes
and no Go fallback.

Run the new Make target with portable defaults, or explicit launchers locally:

```sh
PATH=/opt/homebrew/bin:$PATH make retrieval-document-extraction-differential \
  RETRIEVAL_EXTRACTION_GO='/Users/daniel/.local/bin/dev-external go' \
  CARGO='/Users/daniel/.local/bin/dev-external cargo'
```

The generators require `GOTOOLCHAIN=go1.26.10`; generation additionally requires
`PORT_GENERATE=1`. The Rust replay contains an expected-section mutation control.

## PDF contract

The Go reader is `github.com/ledongthuc/pdf`, revision
`5959a4027728` (module version `v0.0.0-20250511090121-5959a4027728`).
Rust uses lopdf for PDF objects and font decoding, with an explicit port of
`Page.GetPlainText`'s operator policy rather than lopdf's layout/text extractor:
BT starts a newline; T* adds a newline; TJ ignores glyph-position numbers;
apostrophe adds no newline; the Go double-quote arity bug drops that page.
Flattened text uses the already-ported dehyphenator. Sections retain page
numbers, do not dehyphenate, and count byte offsets plus two between nonblank
pages, including when blank pages were skipped.

Seven generated PDF cases establish this limited subset, including WinAnsi.
Complex CMaps/ToUnicode, inherited/absent fonts, content arrays and malformed or
encrypted PDF diagnostics are **not proven equivalent** to ledongthuc/pdf.
Lopdf diagnostic strings beyond the invalid-header case are not Go strings.

## Known residuals / intentional safety differences

- ODF `text:s` repetition is capped at 100 MiB per instruction rather than
  accepting Go's arbitrary `strings.Repeat` allocation. The cap policy has an
  explicit regression; approval of this safety difference is still needed.
- PDF content/font decompression is capped at 100 MiB. Go has no such cap.
  A compressed-stream regression exercises this same limit path at a small
  bound and pins the production limit separately.
- XML tokenization uses quick-xml. Early malformed tokens/entities can have
  different partial-text/read-ahead behavior from Go encoding/xml. Archive
  budget accounting reads capped parts eagerly rather than measuring Go's
  decoder read-ahead; the valid full-part budget corpus matches, not every
  malformed-part budget combination.
- ZIP corruption/CRC/encryption errors and duplicate member names are not yet
  exhaustively replayed; zip crate validation/diagnostics may differ from Go.
- Re-embed archive/PDF inputs currently use `fs::read` before bounded extraction,
  unlike Go's random-access archive reader; very large compressed source files
  can therefore require more source-buffer memory. HTML/RTF reads are capped.
- Native Linux/Windows execution and broader real-document corpus remain open.

The provenance ledger is coordinator-owned. Expanding
`retrieval/reembed-http-cli.json` necessarily invalidates its old checksum in
`testdata/port/provenance.json`; do not suppress that failing identity gate or
claim the full symdesk-index suite passed until the coordinator refreshes it.
The new fixture is registered in portgen's inventory and check list; its
provenance/checksum regeneration also belongs to the integration owner.
