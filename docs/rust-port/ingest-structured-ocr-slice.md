# RUST-010 ingest slice 2: structured readers, ZIP boundary, OCR runner

**Partial implementation; not a cutover gate.** Go production is unchanged and
remains authoritative. This slice closes the byte-level extraction readers, the
ZIP defence boundary (SEC-001 archive subset) and the external-tool OCR runner.

## Structured readers and the ZIP boundary (INGEST-002, SEC-001)

`crates/symdesk-ingest/src/{gxml,gmime,ghtml,gzip,structured}.rs` port the code
the Go readers actually run on:

- `gxml`: Go `encoding/xml` tokenizer semantics (entity tables, charset
  handling, exact syntax-error text with line numbers) behind
  DOCX/XLSX/PPTX/ODF text extraction and `WorkbookSheetNames`.
- `gmime`: Go `net/mail` + `mime` + `mime/multipart` + `mime/quotedprintable`
  header, media-type, boundary, base64 and quoted-printable behaviour used by EML.
- `ghtml`: `html.UnescapeString` (Go's entity table, longest-prefix semantics for
  references without a semicolon).
- `gzip`: Go `archive/zip` open/read/CRC behaviour (EOCD scan, ZIP64 handling,
  directory-header validation, duplicate entry names kept, `ErrFormat`/
  `ErrChecksum`/`ErrAlgorithm`) with the ingest caps (4096 entries, 32 MiB per
  entry, 128 MiB total). Inflate uses `miniz_oxide`.

`TestPortIngestStructuredContract` (`internal/ingest/internal/extract`) freezes
**2,560 cases** into `testdata/port/ingest/structured.json`: 19 corpus, 7 DOCX,
113 EML, 2 EPUB, 105 HTML, 61 NormalizeText, 1 ODS, 1 ODT, 3 PPTX, 11 XLSX,
2,177 direct XML-reader inputs and **60 ZIP defence cases** (bombs at 33 MiB and
256 MiB, aggregate cap crossings 4×30/5×30/4×32 MiB and over-by-4, 4095/4096/4097
entries, per-entry cap ±1, absolute/backslash/traversal names, bad/stored CRC,
size lies, truncations, ZIP64 without extra, duplicate names, two EOCDs, nested
bomb ignored, unsupported methods). Every case records result text, MIME,
engine, language or the exact Go error text; recovered Go panics are recorded.
`make ingest-structured-differential` replays them in Rust
(`tests/structured_contracts.rs`, plus a negative control).

### Declared differences

- Go reports a recovered panic as `extract: panic in Go reader: …`; Rust returns
  the same text as an error instead of unwinding.
- Go's `flate: corrupt input before offset N` carries the input byte offset; the
  Rust inflate rejects the same data as `flate: corrupt input` without the
  offset. The replay accepts exactly that difference (same rejection class, no
  output) and nothing else; this is the only normalised difference in the 60 ZIP
  cases.
- Magika advisory detection (optional subprocess) is not ported; the fixture
  runs Go with an empty PATH so Go never calls it.

## OCR runner (INGEST-003, part)

`crates/symdesk-ingest/src/ocr.rs` ports `internal/ingest/internal/ocr.Runner`:
`tesseract --list-langs` validation with the per-runner cache (including the
cached "cannot validate" state), language fallback warnings, the one-shot
German re-run, image OCR from the image's own directory with a relative name,
HEIC via `sips` into a private temp directory, PDF via `pdftoppm -png -r 150`
then up to four concurrent per-page `tesseract` processes joined in page order,
first-error cancellation, temp-directory cleanup, context cancellation that
kills the child (`signal: killed`), and Go-formatted `exec` errors
(`exec: "x": executable file not found in $PATH`, `is a directory`,
`permission denied`, `fork/exec …: exec format error`).

`TestPortIngestOCRContract` (`internal/ingest/internal/ocr`) drives the real Go
runner against fake tool scripts stored in the fixture and records, for **60
scenarios**, the result or exact error text, the warnings Go writes to stderr,
and a normalized process transcript (argv, cwd, environment probe). The replay
in `tests/ocr_contracts.rs` executes the identical scripts. Cancellation
scenarios cancel when a marker appears in the tool log, so outcomes do not
depend on timing or load. The scripts set their own `PATH=/usr/bin:/bin` because
`portgen --check` runs Go with a restricted PATH.

`make ingest-ocr-differential`, `make ingest-ocr-fixtures-generate`.

### Declared limits

- Real Tesseract/Poppler/sips output is not part of the contract: the adapters
  are exercised through argv/env/cwd/exit/stderr/stdout fakes, which is the
  contract matrix wording ("injected Tesseract/Poppler/Ollama").
- Rust refuses a spawn target that is neither `#!`, ELF nor Mach-O with
  `exec format error` itself, because libc `posix_spawn` would otherwise fall
  back to `/bin/sh` (Go and the kernel do not).
- The module is `cfg(unix)`; the Windows build is not covered.
- Barcode/QR splitting (`internal/ingest/barcode.go`, gozxing), the Ollama VLM
  runner and the pipeline that routes files to engines are **not ported**.

## Acceptance

```
make ingest-differential          # queue, extraction, structured, OCR, Notion, Paperless
cargo nextest run -p symdesk-ingest
cargo +nightly fuzz run mime_input   # fuzz/seeds/mime_input holds fixture-derived seeds
```

`mime_input` drives detection, every byte-oriented reader, the XML tokenizer,
MIME/multipart/quoted-printable/base64 decoding and the ZIP reader (modes 0-7
selected by the first byte; stored ZIPs with valid CRCs are synthesized so the
structured readers are reachable).
