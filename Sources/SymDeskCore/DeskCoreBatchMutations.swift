import Foundation

extension DeskCore {
    /// Per-file outcome of a batch document mutation, mirroring the core's
    /// `doc <mutation> <file...>` JSON payload.
    public struct DocBatchOutcome: Codable, Equatable, Sendable {
        public struct Item: Codable, Equatable, Sendable {
            public let file: String
            public let status: String
            public let error: String?
        }
        public let status: String
        public let updated: Int
        public let failed: Int
        public let results: [Item]
    }

    private func runDocBatch(_ leading: [String], paths: [String], trailing: [String]) async throws -> DocBatchOutcome {
		try await runDecoding(DocBatchOutcome.self, arguments: leading + paths + trailing + ["--json"] + vaultArgs)
    }

    /// Set status on many documents in one core invocation.
    @discardableResult
    public func docSetStatus(paths: [String], status: String) async throws -> DocBatchOutcome {
        try await runDocBatch(["doc", "status"], paths: paths, trailing: [status])
    }

    /// Set due date on many documents in one core invocation.
    @discardableResult
    public func docSetDue(paths: [String], date: String) async throws -> DocBatchOutcome {
        try await runDocBatch(["doc", "due"], paths: paths, trailing: [date])
    }

    /// Set document_type on many documents in one core invocation.
    @discardableResult
    public func docSetType(paths: [String], type: String) async throws -> DocBatchOutcome {
        try await runDocBatch(["doc", "type"], paths: paths, trailing: [type])
    }

    /// Set correspondent on many documents in one core invocation.
    @discardableResult
    public func docSetCorrespondent(paths: [String], name: String) async throws -> DocBatchOutcome {
        try await runDocBatch(["doc", "correspondent"], paths: paths, trailing: [name])
    }

    /// Add a tag to many documents in one core invocation.
    @discardableResult
    public func docAddTag(paths: [String], tag: String) async throws -> DocBatchOutcome {
        try await runDocBatch(["doc", "tag", "add", tag], paths: paths, trailing: [])
    }

    /// Remove a tag from many documents in one core invocation.
    @discardableResult
    public func docRemoveTag(paths: [String], tag: String) async throws -> DocBatchOutcome {
        try await runDocBatch(["doc", "tag", "remove", tag], paths: paths, trailing: [])
    }

    public func docSetASN(path: String, value: String = "next") async throws {
		_ = try await runChecked(arguments: ["doc", "asn", path, value] + vaultArgs)
    }
}
