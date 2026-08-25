import Foundation
import SymairaIngestContract

// DeskCore conforms to ReOCRClient directly (same pattern as
// ClassificationRulesClient in DeskCoreRules.swift): Re-OCR now runs through
// `symdesk ingest reocr` via the already-resolved transport instead of
// locating the retired standalone `symingest` binary (#610). Availability in
// the UI follows whether the core is ready, not whether that binary exists.
extension DeskCore: ReOCRClient {
    public func reprocess(documentID: Int64) async throws -> ReOCRResponse {
        try await runReOCR(arguments: ["ingest", "reocr", "--document-id", String(documentID), "--json"] + vaultArgs)
    }

    public func reprocess(archivePath: String) async throws -> ReOCRResponse {
        try await runReOCR(arguments: ["ingest", "reocr", archivePath, "--json"] + vaultArgs)
    }

    // `ingest reocr` writes its {document_id, status, error} envelope to
    // stdout even on a non-zero exit, so a failure (e.g. no archived
    // original) still reports which document failed and why (see
    // cmd/symdesk/ingest.go's reportReocrError, issue #438's contract).
    // `runChecked` would throw the moment the exit code is non-zero and
    // discard that stdout, so this reads via `commandResult` and decodes
    // stdout regardless of exit status — only falling back to a generic
    // execution-failed error when stdout isn't a valid envelope at all.
    private func runReOCR(arguments: [String]) async throws -> ReOCRResponse {
        guard let transport else { throw DeskCoreError.coreNotFound }
        let result = try await transport.commandResult(arguments: arguments)
        do {
            return try decodeSchemaChecked(result.stdout)
        } catch {
            throw DeskCoreError.cliExecutionFailed(exitCode: result.exitCode, stderr: result.stderrText)
        }
    }
}
