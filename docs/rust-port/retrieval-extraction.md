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

The Go reader is `github.com/ledongthuc/pdf`, revision `5959a4027728`
(module version `v0.0.0-20250511090121-5959a4027728`). Rust no longer relies on
lopdf's text extraction. `retrieval_pdf.rs` ports the path that decides which
text comes out and keeps lopdf only for object and cross-reference access:

- **Open checks** (`NewReaderEncrypted`): header `%PDF-1.[0-7]` followed by CR
  or LF (so `%PDF-1.8`, `%PDF-2.0` and `%PDF-1.4 ` are rejected like Go),
  `%%EOF` in the last 100 bytes, the `startxref` line and its integer, with
  Go's error wording. Cross-reference syntax errors beyond that still carry
  lopdf's wording.
- **Pages**: `Root.Pages.Count` and the `Count`-based `Page(i)` walk, resource
  inheritance through `Parent`, `Contents` as one stream or an array that
  shares one operand stack.
- **Lexer and `Interpret`**: the Go content-stream lexer (comments, literal
  strings with its escape rules, hex strings, names with `#xx`, numbers,
  inline arrays and dictionaries) and the PostScript helpers cmaps use
  (`dict`, `begin`, `end`, `def`, `currentdict`, `pop`). Every Go panic (bad
  escape, odd or invalid hex, stray `]` or `>`, integer overflow, wrong operand
  count for `Tf`, `Tj`, `'`, `"`) drops the page, like `recover` in
  `GetPlainText`; `BT` adds a newline, `T*` decodes `"\n"` through the current
  encoder, `TJ` ignores positioning numbers and a non-array operand.
- **Encoders** (`Font.Encoder`): `WinAnsiEncoding`, `MacRomanEncoding`,
  `Identity-H` and an absent `/Encoding` through `ToUnicode` (Go's cmap reader:
  codespace ranges of one to four bytes, `bfchar`, `bfrange` with string or
  array destination, UTF-16 decoding, unmapped codes become U+FFFD), a
  dictionary `/Encoding` through `/Differences` and Go's glyph-name table,
  the PDFDoc table as the `ToUnicode`-less fallback and the identity encoder
  for any other name. The tables are generated from the Go sources by
  `scripts/rust-port/gen_pdf_go_tables.py` into `pdf_go_tables.rs`.
- **Filters**: `FlateDecode` (PNG-Up predictor 12 only) like Go; every other
  filter fails the page.

`TestRetrievalExtractionFixture` now records 126 cases, 73 of them PDFs: the
original seven plus 66 font, encoding, lexer, structure and open-check cases
(ToUnicode with `bfrange` arrays and offsets, surrogate pairs, odd UTF-16,
missing codespace ranges, `Differences` through indirect objects, MacRoman,
StandardEncoding, Type0 with and without `ToUnicode`, inherited resources,
nested page trees, wrong `Count`, `Contents` arrays, Flate with and without a
predictor, LZW and corrupt Flate, content lexer oddities, PDF 1.7/1.8/2.0 and
CRLF headers, `%%EOF` junk and whitespace). All 73 match in the Rust replay
(`make retrieval-document-extraction-differential`).

Not proven equivalent: ASCII85 (Go's `alphaReader` blanks every non-ASCII85 byte
to NUL, so real streams fail there; Rust fails the page for the whole filter),
xref/trailer syntax errors past `startxref`, xref streams and object streams,
encrypted PDFs, `Length` that disagrees with `endstream`, and content streams
on which Go loops forever (an unterminated array, hex string or cmap count;
Rust returns a page error instead, and no fixture contains them). Lopdf
diagnostic strings beyond the header/EOF/startxref checks are not Go strings.

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
