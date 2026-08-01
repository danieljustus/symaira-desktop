import Foundation

/// The kind of vault represented by a registry entry.
public enum VaultEntryKind: String, Codable, Sendable {
    case local
    case server
}

/// A named vault that can be selected by the client.
///
/// Local entries retain both the user-visible path and its security-scoped
/// bookmark. Server entries retain only their endpoint; authentication tokens
/// remain in the Keychain and are deliberately not part of this Codable type.
public struct VaultEntry: Codable, Equatable, Hashable, Identifiable, Sendable {
    public typealias Kind = VaultEntryKind

    public let id: UUID
    public var name: String
    public let kind: Kind
    public var path: String?
    public var bookmarkData: Data?
    public var serverURL: URL?
    public var isDemoMode: Bool
    public var lastOpenedAt: Date?

    public init(
        id: UUID = UUID(),
        name: String,
        kind: Kind,
        path: String? = nil,
        bookmarkData: Data? = nil,
        serverURL: URL? = nil,
        isDemoMode: Bool = false,
        lastOpenedAt: Date? = nil
    ) {
        self.id = id
        self.name = name
        self.kind = kind
        self.path = path
        self.bookmarkData = bookmarkData
        self.serverURL = serverURL
        self.isDemoMode = isDemoMode
        self.lastOpenedAt = lastOpenedAt
    }

    /// Creates a local vault entry.
    public static func local(
        name: String,
        path: String?,
        bookmarkData: Data? = nil,
        isDemoMode: Bool = false,
        lastOpenedAt: Date? = nil
    ) -> VaultEntry {
        VaultEntry(
            name: name,
            kind: .local,
            path: path,
            bookmarkData: bookmarkData,
            isDemoMode: isDemoMode,
            lastOpenedAt: lastOpenedAt
        )
    }

    /// Creates a server vault entry. The server token is intentionally not
    /// accepted here; it is managed by ``ServerConnectionConfig``.
    public static func server(
        name: String,
        url: URL,
        lastOpenedAt: Date? = nil
    ) -> VaultEntry {
        VaultEntry(
            name: name,
            kind: .server,
            serverURL: url,
            lastOpenedAt: lastOpenedAt
        )
    }

    /// Convenience alias for callers that use the endpoint terminology.
    public var url: URL? { serverURL }
}

/// Persists named local and server vaults in UserDefaults.
///
/// The registry is intentionally independent of the active-vault lifecycle.
/// It supplies durable metadata for the later UI switcher while leaving
/// `VaultConfig` and `ServerConnectionConfig` as the active connection owners.
public final class VaultRegistry {
    /// UserDefaults key for the JSON-encoded registry.
    public static let registryDefaultsKey = "symdesk.vaultRegistry.v1"

    private let defaults: UserDefaults

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    /// Returns all saved entries, migrating the legacy local-vault keys when
    /// the registry has not yet been written.
    ///
    /// `now` is injectable so migration and last-opened behavior are
    /// deterministic in tests. A present but invalid registry is treated as
    /// unreadable and returned as an empty list; it is never replaced by a
    /// best-effort legacy migration.
    public func entries(now: Date = Date()) -> [VaultEntry] {
        if defaults.object(forKey: Self.registryDefaultsKey) != nil {
            guard let data = defaults.data(forKey: Self.registryDefaultsKey),
                  let decoded = decodeEntries(from: data) else {
                return []
            }
            return decoded
        }

        let migrated = migrateLegacyLocalEntry(now: now)
        if !migrated.isEmpty {
            persist(migrated)
        }
        return migrated
    }

    /// Replaces the registry contents without changing any legacy keys.
    public func save(_ entries: [VaultEntry]) {
        persist(entries)
    }

    /// Updates the last-opened timestamp for an entry and persists the change.
    /// Returns the updated entry, or `nil` when the identifier is not present.
    @discardableResult
    public func recordOpened(_ id: UUID, at date: Date = Date()) -> VaultEntry? {
        var current = entries(now: date)
        guard let index = current.firstIndex(where: { $0.id == id }) else {
            return nil
        }
        current[index].lastOpenedAt = date
        persist(current)
        return current[index]
    }

    private func migrateLegacyLocalEntry(now: Date) -> [VaultEntry] {
        let path = defaults.string(forKey: VaultConfig.Key.vaultPath)
        let bookmarkData = defaults.data(forKey: VaultConfig.Key.vaultBookmark)
        let hasDemoFlag = defaults.object(forKey: VaultConfig.Key.isDemoMode) != nil

        guard path != nil || bookmarkData != nil || hasDemoFlag else {
            return []
        }

        let name: String
        if let path, !path.isEmpty {
            let lastPathComponent = URL(fileURLWithPath: path).lastPathComponent
            name = lastPathComponent.isEmpty ? "Local Vault" : lastPathComponent
        } else {
            name = "Local Vault"
        }

        return [VaultEntry.local(
            name: name,
            path: path?.isEmpty == true ? nil : path,
            bookmarkData: bookmarkData,
            isDemoMode: defaults.bool(forKey: VaultConfig.Key.isDemoMode),
            lastOpenedAt: now
        )]
    }

    private func persist(_ entries: [VaultEntry]) {
        guard let data = try? JSONEncoder().encode(entries) else { return }
        defaults.set(data, forKey: Self.registryDefaultsKey)
    }

    private func decodeEntries(from data: Data) -> [VaultEntry]? {
        if let entries = try? JSONDecoder().decode([VaultEntry].self, from: data) {
            return entries
        }

        // Accept the versioned wrapper as well so a future schema can add
        // metadata without making an existing registry unreadable.
        struct VersionedRegistry: Codable {
            let entries: [VaultEntry]
        }
        return try? JSONDecoder().decode(VersionedRegistry.self, from: data).entries
    }
}
