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
evidence before removing Go. This change does not promote dataset sync,
malformed-UTF-8 parity, resource bounds, or migration acceptance.
