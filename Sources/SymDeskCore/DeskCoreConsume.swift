import Foundation

extension DeskCore {
    /// Status of the consume (watched inbox) folder.
    public struct ConsumeFolderStatus: Codable, Sendable, Equatable {
        public let inboxPath: String
        public let configuredPath: String
        public let exists: Bool
        public let vaultPath: String

        enum CodingKeys: String, CodingKey {
            case inboxPath = "inbox_path"
            case configuredPath = "configured_path"
            case exists
            case vaultPath = "vault_path"
        }

        public init(inboxPath: String, configuredPath: String, exists: Bool, vaultPath: String) {
            self.inboxPath = inboxPath
            self.configuredPath = configuredPath
            self.exists = exists
            self.vaultPath = vaultPath
        }
    }

    /// Returns the current consume folder configuration from the CLI.
    public func getConsumeFolderStatus() async throws -> ConsumeFolderStatus {
        try await runDecoding(ConsumeFolderStatus.self, arguments: ["consume", "status", "--json"] + vaultArgs)
    }

    /// Sets the consume folder path in the symdesk config.
    public func setConsumeFolderPath(_ path: String) async throws {
        _ = try await runChecked(arguments: ["consume", "set-path", path] + vaultArgs)
    }
}
