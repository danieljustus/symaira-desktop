import Foundation

extension DeskCore {
    /// Per-file outcome of a vault-wide tag operation.
    public struct TagOpOutcome: Codable, Equatable, Sendable {
        public struct Item: Codable, Equatable, Sendable {
            public let file: String
            public let status: String // "updated" | "skipped" | "error"
            public let error: String?
        }
        public let items: [Item]
        public var updatedCount: Int { items.filter { $0.status == "updated" }.count }
    }

    /// Renames a tag across the whole vault, rewriting frontmatter and
    /// re-indexing every carrier so no stale index rows remain.
    @discardableResult
    public func renameTag(from old: String, to new: String) async throws -> TagOpOutcome {
        try await runTagOp(["tags", "rename", old, new])
    }

    /// Merges one tag into another across the whole vault and re-indexes.
    @discardableResult
    public func mergeTag(from: String, into: String) async throws -> TagOpOutcome {
        try await runTagOp(["tags", "merge", from, into])
    }

    /// Deletes a tag from every file across the whole vault and re-indexes.
    @discardableResult
    public func deleteTag(_ tag: String) async throws -> TagOpOutcome {
        try await runTagOp(["tags", "delete", tag])
    }

    private func runTagOp(_ arguments: [String]) async throws -> TagOpOutcome {
        let items: [TagOpOutcome.Item] = try await runDecoding(
            [TagOpOutcome.Item].self,
            arguments: arguments + ["--json"] + vaultArgs
        )
        return TagOpOutcome(items: items)
    }

    public func docsSimilar(path: String, threshold: Int = 50) async throws -> [SimilarDoc] {
		try await runDecoding([SimilarDoc].self, arguments: ["similar", path, "--threshold", "\(threshold)", "--json"] + vaultArgs)
    }

    /// One cluster of possible duplicates from the vault-wide SimHash scan.
    public struct DuplicateGroup: Codable, Equatable, Identifiable, Sendable {
        public struct Member: Codable, Equatable, Sendable {
            public let path: String
            public let title: String
            public let similarity: Int

            public init(path: String, title: String, similarity: Int) {
                self.path = path
                self.title = title
                self.similarity = similarity
            }
        }
        public let path: String
        public let title: String
        public let members: [Member]
        public var id: String { path }
    }

    /// Minimum similarity, in percent, two documents must reach before they
    /// are offered as possible duplicates.
    ///
    /// The Possible Duplicates screen asks the reader to merge a group and
    /// trash the rest, so a false grouping costs more than a missed one. At 50
    /// every document sharing a frontmatter and heading skeleton landed in one
    /// group — a medical letter beside a bank statement, at 51-67% similarity
    /// (issue #439).
    ///
    /// 85 is measured, not guessed. Against a vault holding one near-identical
    /// pair (two invoices differing in an amount and a month) plus two
    /// unrelated letters, the pair alone is grouped for every threshold from
    /// 65 to 85; at 60 and below the unrelated letters join it, and at 90 even
    /// the near-identical pair is missed. 85 is therefore the strictest bar
    /// that still catches a real duplicate.
    public static let defaultDuplicateThreshold = 85

    /// Built separately from `duplicates` so the requested threshold is
    /// testable without spawning the CLI.
    static func duplicatesArguments(threshold: Int, vaultArgs: [String]) -> [String] {
        ["duplicates", "--threshold", "\(threshold)", "--json"] + vaultArgs
    }

    /// Scans the whole vault for groups of possible duplicate documents.
    public func duplicates(threshold: Int = DeskCore.defaultDuplicateThreshold) async throws -> [DuplicateGroup] {
        try await runDecoding(
            [DuplicateGroup].self,
            arguments: Self.duplicatesArguments(threshold: threshold, vaultArgs: vaultArgs)
        )
    }

    /// Result of exporting a note or view to PDF/HTML.
    public struct ExportResult: Codable, Equatable, Sendable {
        public let format: String
        public let path: String
        public let profile: String?
        public let rendered: Bool
        public let message: String?
    }

    /// Exports a vault-relative note to PDF or HTML via the core CLI.
    public func exportNote(path: String, format: String, outputPath: String) async throws -> ExportResult {
        try await runDecoding(
            ExportResult.self,
            arguments: ["export", "--note", path, "--format", format, "--output", outputPath, "--json"] + vaultArgs
        )
    }

    public func docsReview(threshold: Int = 70) async throws -> [ReviewDoc] {
		try await runDecoding([ReviewDoc].self, arguments: ["docs", "review", "--threshold", "\(threshold)", "--json"] + vaultArgs)
    }
}
