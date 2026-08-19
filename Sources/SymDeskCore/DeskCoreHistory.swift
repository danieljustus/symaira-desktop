import Foundation

extension DeskCore {
    public func historyList(path: String) async throws -> [HistoryEntry] {
        try await runDecoding([HistoryEntry].self, arguments: ["history", path, "--json"] + vaultArgs)
    }

    /// Returns the stored content of a snapshot, used to render a diff
    /// between a version and the current file (issue #307).
    public func historyContent(id: String) async throws -> String {
        struct HistoryShowPayload: Codable { let content: String }
        let payload: HistoryShowPayload = try await runDecoding(
            HistoryShowPayload.self,
            arguments: ["history", "show", id, "--json"] + vaultArgs
        )
        return payload.content
    }

    public func historyRestore(path: String, at id: String = "") async throws {
        var args = ["restore", path]
        if !id.isEmpty {
            args += ["--at", id]
        }
        args += ["--json"] + vaultArgs
        _ = try await runChecked(arguments: args)
    }
}
