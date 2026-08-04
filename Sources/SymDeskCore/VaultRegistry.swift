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

    /// Appends an entry when no entry with the same id exists yet, otherwise
    /// replaces the stored entry (same id keeps its position). Persists the
    /// change. Returns the stored entry.
    @discardableResult
    public func upsert(_ entry: VaultEntry) -> VaultEntry {
        var current = entries()
        if let index = current.firstIndex(where: { $0.id == entry.id }) {
            current[index] = entry
        } else {
            current.append(entry)
        }
        persist(current)
        return entry
    }

    /// Removes an entry from the registry. Never touches the vault folder or
    /// the server connection — it only forgets the entry (issue #296).
    public func remove(id: UUID) {
        var current = entries()
        current.removeAll { $0.id == id }
        persist(current)
    }

    /// Renames an entry in place. Returns the updated entry, or `nil` when
    /// the identifier is not present.
    @discardableResult
    public func rename(id: UUID, to name: String) -> VaultEntry? {
        var current = entries()
        guard let index = current.firstIndex(where: { $0.id == id }) else {
            return nil
        }
        current[index].name = name
        persist(current)
        return current[index]
    }

    /// Returns the entry with the given id, if present.
    public func entry(id: UUID) -> VaultEntry? {
        entries().first { $0.id == id }
    }

    /// Returns the local entry for a vault path, if one is registered.
    /// Paths are compared after standardizing, so a trailing slash or a
    /// `..` component does not hide an existing entry.
    public func localEntry(path: String) -> VaultEntry? {
        let normalized = standardized(path)
        return entries().first { entry in
            entry.kind == .local && entry.path.map(standardized) == normalized
        }
    }

    /// The most recently opened entry of either kind, or `nil` when the
    /// registry is empty. Used on relaunch to reopen the last active vault.
    public func mostRecentlyOpened(now: Date = Date()) -> VaultEntry? {
        entries(now: now)
            .filter { $0.lastOpenedAt != nil }
            .max { ($0.lastOpenedAt ?? .distantPast) < ($1.lastOpenedAt ?? .distantPast) }
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

    /// Creates or updates a local entry for a freshly picked vault folder.
    /// Reuses an existing entry when the path is already registered so the
    /// registry never accumulates duplicates of the same folder.
    public func registerLocal(
        name: String,
        path: String,
        bookmarkData: Data?,
        isDemoMode: Bool = false,
        now: Date = Date()
    ) -> VaultEntry {
        let normalized = standardized(path)
        let existing = entries(now: now).first { entry in
            entry.kind == .local && entry.path.map(standardized) == normalized
        }
        let entry: VaultEntry
        if let existing {
            entry = VaultEntry(
                id: existing.id,
                name: name.isEmpty ? existing.name : name,
                kind: .local,
                path: path,
                bookmarkData: bookmarkData ?? existing.bookmarkData,
                isDemoMode: isDemoMode,
                lastOpenedAt: now
            )
        } else {
            entry = VaultEntry.local(
                name: name.isEmpty ? URL(fileURLWithPath: path).lastPathComponent : name,
                path: path,
                bookmarkData: bookmarkData,
                isDemoMode: isDemoMode,
                lastOpenedAt: now
            )
        }
        return upsert(entry)
    }

    private func standardized(_ path: String) -> String {
        URL(fileURLWithPath: path).standardizedFileURL.path
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
