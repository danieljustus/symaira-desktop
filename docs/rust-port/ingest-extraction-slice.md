# INGEST-002 local extraction slice

**Partial implementation; not a cutover gate.** Go production remains unchanged.

`crates/symdesk-ingest/src/extract.rs` adds byte-oriented, caller-bounded local
extraction and content-authoritative file detection. It reuses `symdesk-core`'s
format registry and dehyphenation, plus existing locked ZIP/XML/HTML libraries.
`Extraction` carries text, normalized MIME, engine and empty non-OCR language.

## Executed oracle

The opt-out-free `TestPortIngestExtractionContract` invokes real Go `Detect`,
`ReadTextKind` and `ReadStructuredKind` over 84 deterministic synthesized inputs.
No OCR/barcode/PDF engine is created. A private empty PATH disables optional
Magika advisory execution. Production-source identity comes from `fixtureoracle`;
per-file hashes additionally bind the Go readers and test generator. The only
fixture normalizations are source paths in diagnostics and Go JSON's invalid
UTF-8 replacement semantics.

The committed corpus covers format suffixes (including uppercase), PDF/image
signatures, ZIP mimetype/OPC relationships/content types/main parts, OLE legacy
stream identities, misleading suffixes, unknown/empty/corrupt containers, blocked
formats, raw text/Markdown/CSV, HTML, RTF, ODT/ODS/ODP, PPTX slide sorting and EPUB
chapter sorting/DRM. Missing parts, unclosed ODF XML, ZIP entry count and per-entry
size failures replay exact Go error text. Generation was run twice byte-identically;
Go check mode rejected a modified fixture and passed after restoration. Rust also
mutates a successful text expectation through the same comparator.

```
make ingest-extraction-differential
make ingest-extraction-fixtures-generate  # explicit fixture write
```

On the migration host supply `INGEST_GO='/Users/daniel/.local/bin/dev-external go'`
and `CARGO='/Users/daniel/.local/bin/dev-external cargo'`, with Homebrew on PATH.
All compiler invocations used the launcher, Go 1.26.10 and pinned Rust 1.98.0.

## Boundaries and remaining work

- DOCX table extraction, XLSX workbook/shared-string/table extraction and EML
  multipart/transfer decoding remain **unimplemented**, with explicit residual
  errors pinned by a Rust test. Detection of DOCX/XLSX is implemented, not their
  extraction. This is not completion of RUST-010 / INGEST-002.
- No filesystem-opening adapter or Rust pipeline routing was added. The Go
  pipeline calls `extractText` directly for text/Markdown/CSV and delegates other
  kinds to its engine; the existing Go path must remain authoritative.
- No optional Magika subprocess/advisory parsing port. Protocol `mime.rs` is
  system MIME registration, not ingest signature/container detection, so it is
  not an interchangeable primitive.
- Retrieval extraction cannot be substituted unchanged: its DOM HTML reader,
  normalization, DOCX/XLSX table representation, ZIP limits (10/100 MiB), parse
  recovery and errors differ from ingest. Retrieval files/behavior were untouched.
- Broad malformed XML diagnostics, invalid entities/encodings, malformed ZIP/CRC/
  compression diagnostics and repeated ZIP-name behavior need more Go vectors
  before full parser parity is claimed. EOF diagnostics in the committed case
  match; other quick-xml errors are currently native diagnostics.
- Archive aggregate-byte enforcement is implemented, but the 128 MiB aggregate
  crossing still needs an executed Go-generated corpus case. Current parity
  cases prove 32 MiB per-entry and 4096-entry caps, not the aggregate crossing.
- Safety divergences have named Rust tests: caller-supplied source limit (Go's
  text reader is unbounded); malformed OLE exponent/offset handling is checked
  rather than blindly allocating; Rust Unicode strings replace invalid source
  UTF-8 (Go strings can preserve raw bytes). Keep original source bytes separately.
- Only local macOS execution is verified; native Linux/Windows replay remains.
- Central provenance was intentionally not edited. Coordinator must regenerate
  generator digest/checksum inventory after integration.
