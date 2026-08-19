import Foundation

extension DeskCore {
    public func trashList() async throws -> [TrashEntry] {
        try await runDecoding([TrashEntry].self, arguments: ["trash", "list", "--json"] + vaultArgs)
    }

    public func trashRestore(name: String) async throws {
        _ = try await runChecked(arguments: ["trash", "restore", name, "--json"] + vaultArgs)
    }

    public func trashPurgeAll() async throws {
        _ = try await runChecked(arguments: ["trash", "purge", "--all", "--json"] + vaultArgs)
    }

    public func noteDelete(path: String) async throws {
        _ = try await runChecked(arguments: ["delete", path, "--json"] + vaultArgs)
    }
}
