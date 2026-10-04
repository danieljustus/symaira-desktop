# Native DatasetImport rejection replay (#1026)

The five `error_cases` in `testdata/port/dataset/sync.json` now execute the real
Rust `DatasetSyncService::import_csv` boundary in `dataset_import_contracts`.
The parser/projection helper suite remains a separate contract.

The test first invokes the current Go `DatasetImport` service through
`TestPortDatasetImportServiceRejections`. That test reproduces every recorded
case and emits a fresh JSON error oracle only when the caller supplies an owned
`PORT_DATASET_ERROR_FIXTURE` path. Unix errors must still match the checked-in
ledger exactly. Windows compares Rust to actual native Go output, including
`GetFileAttributesEx`, the native path separator, and the OS error message;
POSIX file modes do not prevent this error-only replay.

Only each fixture's own temporary root is replaced by `<tmp>`. All five Rust
errors are compared in full. Every rejected operation must leave the vault
empty and the dataset's sidecar rows absent. A new shared path-error presenter
retains the existing retention error behavior and supplies the missing-source
import error's Go wrapper and OS text.

This replay currently requires Go in the native test environment, consistent
with the migration's live differential phase. The Go-free corpus work in
#1153/#934 must replace that dependency with accepted immutable platform
evidence before removing Go.

## Raw CSV bytes

`testdata/port/dataset/import.json` adds fourteen actual-Go cases to the original
eight source-import cases. They cover malformed text, identity and header bytes,
duplicate headers after Go rune folding, typed conversion failures, byte-column
quote diagnostics, quoted CRLF, raw-byte hash lengths, a valid replacement-rune
control, long binary YAML keys and natural YAML key ordering. Go generated these
cases before the Rust repair. The old Rust importer failed the raw-byte cases
at its whole-file UTF-8 conversion; the repaired importer executes all 22 cases.

The shared CSV lexer preserves field bytes. The import-specific representation
retains raw headers, labels, values and identities until each output boundary:
hashes use original byte lengths and ordering, YAML uses Go's binary scalar
encoding, and SQLite keys and identities remain TEXT with their original bytes.
The replay reads SQLite TEXT bytes directly and compares base64 identities when
the Go-owned state records malformed strings. It also compares every CSV and
Markdown byte, file size/hash, import result and projected JSON string. JSON
replaces each invalid byte separately; a literal valid U+FFFD remains distinct.

Projection remains fallible. The existing nonfinite-number case still writes
the raw CSV and Markdown handle before JSON projection fails, leaving the
sidecar rows unchanged. Parser and typed conversion rejections write nothing.

This is the `DatasetImport` helper scope. Production `DatasetSync`, native
CLI/MCP exposure, storage/resource-bound decisions and migration acceptance
remain separate. Dedicated current-revision native acceptance is required on
both architectures of Linux, macOS and Windows before closing #1026; a local
Linux pass or an older native run does not certify this repair.
