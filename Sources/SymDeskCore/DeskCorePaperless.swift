import Foundation

extension DeskCore {
    /// Per-document outcome of a Paperless import.
    public struct PaperlessImportResult: Codable, Equatable, Sendable {
        public let action: String // "created" | "updated" | "skipped_idempotent" | "error"
        public let paperlessID: Int
        public let title: String
        public let notePath: String?
        public let asn: Int?
        public let error: String?

        enum CodingKeys: String, CodingKey {
            case action
            case paperlessID = "paperless_id"
            case title
            case notePath = "note_path"
            case asn
            case error
        }
    }

    /// Aggregate summary of a Paperless import run.
    public struct PaperlessImportSummary: Codable, Equatable, Sendable {
        public let total: Int
        public let created: Int
        public let updated: Int
        public let skipped: Int
        public let errors: Int
        public let results: [PaperlessImportResult]
    }

    /// Runs the Paperless-ngx export import through the core CLI. In dry-run
    /// mode nothing is written; the summary reports what would happen.
    public func paperlessImport(exportDir: String, dryRun: Bool = false) async throws -> PaperlessImportSummary {
        var args = ["paperless", "import", exportDir, "--json"]
        if dryRun { args.append("--dry-run") }
        return try await runDecoding(PaperlessImportSummary.self, arguments: args + vaultArgs)
    }
}
