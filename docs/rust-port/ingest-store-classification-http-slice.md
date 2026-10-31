# RUST-010 slice 3: whole store, classification and rules, upload parity

**Partial implementation; not a cutover gate.** Go production remains unchanged
and authoritative. This slice closes INGEST-001, INGEST-006 and HTTP-004 locally
and records, row by row, what keeps INGEST-002, -003, -004 and -005 open.

## INGEST-001: queue and the whole Go `store.Store` (PASS locally)

`queue.json` (75 steps) already froze the job state machine. The audit against
the full Go store found the rest of `store.go` unported: classification rules,
document extractions, mail poll status and cursor, `ListJobs`/`ListJobsInVault`,
claim ordering and reopen/migration idempotency.

`TestPortIngestStoreContract` (`internal/ingest/internal/store`, flag
`-port-ingest-store`) drives the real Go store with a fake SQLite clock through
108 steps and records, after every step, the typed result bytes, the error text
and **every table** (including `schema_migrations`): rule validation/add/update/
delete (unknown ids, unicode, 5,000-byte patterns, autoincrement after delete),
claim order across `created_at`, listings with every limit/offset/vault form,
extraction record/list (span or none, FK failure, partial batches), poll
status/cursor (UTC, nanoseconds, `uint32` extremes, upserts), mail dedupe and a
close/reopen. `symdesk_ingest::{ClassificationRule, Extraction, MailPollStatus,
MailPollCursor}` and the new `Store` methods replay it
(`tests/store_contracts.rs`, `make ingest-store-differential`). A mutated
expectation is rejected (negative control run with `INGEST_STORE_FIXTURE`).

Declared: `time.Time` parameters are stored by modernc as
`2006-01-02 15:04:05.999999999 -0700 MST`; Rust writes the same text for UTC and
`+hhmm +hhmm` for other offsets (Rust has no zone abbreviations), and the reader
accepts Go's form including a `m=±` monotonic suffix.

## INGEST-006: classification, rule test and dry run (PASS locally)

Path templates and `Disambiguate` were frozen in slice 1; `internal/templatepath`
has no ingest call site in Go (only `config` validates it). What remained was the
classification the ingest pipeline applies and the facade's rule tooling.

- `TestPortIngestClassificationContract` (`internal/ingest`) drives the real
  facade (`Rules`, `AddRule`, `TestRules`, `DryRunRule`) over a real store and
  real note files: 48 steps, including Go's per-rune simple lowercasing (`İ`,
  `ß`, final sigma, titlecase `ǅ`), invalid UTF-8 and NUL in the text, frontmatter
  stripping (CRLF, unterminated, empty, no blank line), missing/directory note
  paths (exact `open`/`read` error text) and rule validation errors.
- `TestPortIngestClassifyPipelineContract` (`internal/ingest/internal/ingest`)
  runs the real `Pipeline.Ingest` for 15 text/Markdown/CSV sources with 13 rules
  and presets and records result and persisted document columns: first-wins
  scalars, tag de-duplication, preset precedence and replacement of tags.
- `symdesk_ingest::classification` (`classify`, `test_rules`, `dry_run_rule`,
  `note_body`) replays both (`tests/classification_contracts.rs`,
  `make ingest-classification-differential`), with a negative control.

Declared: the Rust replay feeds the *extracted text* (checked equal to Go's) to
`classify`; the Rust pipeline that calls it does not exist yet (see below). Note
bytes read lossily as UTF-8 in the dry run (Go would keep invalid bytes). Windows
`filepath` semantics for the note title are not covered.

## HTTP-004: upload/multipart and configured-user routes (PASS locally)

`POST /api/v1/ingest` now uses `symdesk_ingest::gform`, a port of
`ParseMultipartForm` + `FormFile("file")` over the existing ported
`mime/multipart` reader (replacing the axum/multer extractor, whose framing and
parameter rules differ from Go's). The live Go/Rust loopback differential gained
**58 cases** (`make http-differential`; all 291 transcripts pass):

- filenames: traversal, backslash and Windows paths, `.`/`..`/blank/padded,
  trailing slash, nested, quote, control character, Unicode, 300-byte (exact
  `renameat … file name too long`), RFC 2231 `filename*`, empty filename, field
  without filename;
- structure: wrong field, two files (first wins), fields before the file,
  preamble/epilogue, LF-only framing, quoted/space boundaries, missing closing
  boundary, missing boundary, `multipart/mixed`, JSON/urlencoded/missing
  Content-Type, folded and lower-case part headers, transfer-encoding ignored,
  binary content that resembles boundaries;
- query: Go reports a malformed escape or a semicolon in the query only after a
  successful parse (`ParseMultipartForm` returns `parseFormErr`), which is 413;
- limits: a body past 101 MiB (413), a file of exactly 100 MiB (202) and 100 MiB
  + 1 byte (500 `upload exceeds 100 MiB`), real 100 MiB transfers on both servers;
- principals: named user and named worker on ingest (403), wrong credential, HEAD,
  PUT and trailing-slash routing; and every persisted upload is compared across
  the two vaults (bytes, 0640/0600 modes, job file against response).

Found and fixed by the differential: `filepath.Base` of `dir/` (Go keeps `dir`),
the rename error text/temporary-file name length, and **named users without the
`worker` role may use every `/api/v1/worker/*` route** (Go checks no role there;
Rust returned 403).

Declared: the harness bounds `body_repeat` at 102 MiB only for the ingest path and
uses a 120 s client timeout for those two cases (debug builds need more than the
default 5 s); the long-filename error body is compared after replacing the random
temporary/upload ids.

## Still open (rows stay TODO)

- **INGEST-002**: Magika advisory detection, the PDF text engine and the routing of
  kinds to engines (the pipeline).
- **INGEST-003**: barcode/QR splitting (gozxing), the Ollama VLM runner, pipeline
  wiring.
- **INGEST-004**: the IMAP session (TLS/auth/UID search/fetch/store/move), the poll
  loop, the attachment reader (`go-message`) and the directory watcher. The store
  side (UID cursor, poll status, dedupe) is frozen above.
- **INGEST-005**: audit result. Notion `Run` and the Paperless mapping, state,
  HTTP and export slices are ported and replayed. The Paperless `Run`, `importOne`,
  `Verify`, `selectDocuments`, `loadLookups` and `WriteMigrationReport`
  orchestration and the Notion migration report writer need the Rust ingest
  pipeline and note writer (`Pipeline.Ingest`, `writer.NoteWriter`,
  `annotate`), which do not exist yet. That pipeline is the shared prerequisite of
  INGEST-002/003/005.

## Acceptance

```
make ingest-differential        # queue, store, classification, extraction, structured, OCR, Notion, Paperless
make http-differential PORT_LEFT=bin/port/symdesk-go PORT_RIGHT=<target>/debug/symdesk
cargo nextest run -p symdesk-ingest -p symdesk-protocol
```
