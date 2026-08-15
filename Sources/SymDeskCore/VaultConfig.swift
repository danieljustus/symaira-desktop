import Foundation

/// Manages the vault location configuration for SymDesk.
///
/// Stores the chosen vault path in UserDefaults along with a security-scoped
/// bookmark so the path persists across app restarts even under sandboxing.
/// Every CLI invocation appends `--vault <path>` when a vault is configured.
public struct VaultConfig {

    // MARK: - UserDefaults Keys

    // Kept internal so the registry migration reads the legacy keys from one source.
    enum Key {
        static let vaultPath = "symdesk.vaultPath"
        static let vaultBookmark = "symdesk.vaultBookmark"
        static let isDemoMode = "symdesk.isDemoMode"
        static let finderFavoritesEnabled = "symdesk.finderFavoritesEnabled"
    }

    // MARK: - Public API

    /// Returns the currently configured vault path, or `nil` if no vault is set.
    ///
    /// If a security-scoped bookmark was saved, the bookmark is resolved first
    /// and the resolved path is returned.  If bookmark resolution fails the
    /// stored string path is returned as a fallback.
    public static func vaultPath() -> String? {
        // Try bookmark resolution first (works under sandbox)
        if let bookmarkData = UserDefaults.standard.data(forKey: Key.vaultBookmark) {
            var isStale = false
            do {
                let url = try URL(
                    resolvingBookmarkData: bookmarkData,
                    options: [],
                    relativeTo: nil,
                    bookmarkDataIsStale: &isStale
                )
                if isStale {
                    // Re-save a fresh bookmark
                    saveBookmark(for: url)
                }
                return url.path
            } catch {
                // Bookmark resolution failed; fall through to string fallback
            }
        }

        let path = UserDefaults.standard.string(forKey: Key.vaultPath)
        return path?.isEmpty == true ? nil : path
    }

    /// Whether the user is currently in demo mode.
    public static var isDemoMode: Bool {
        UserDefaults.standard.bool(forKey: Key.isDemoMode)
    }

    /// Whether a vault has been configured (either real or demo).
    public static var hasConfiguredVault: Bool {
        vaultPath() != nil || ServerConnectionConfig.hasConnection
    }

    /// Whether `path` cannot be opened as a vault because nothing is there any
    /// more, or what is there is not a directory.
    ///
    /// Restoring a vault whose folder had been deleted presented it as a
    /// valid, empty vault and invited the user to create notes inside a
    /// directory that no longer exists (issue #444).
    public static func isVaultDirectoryMissing(_ path: String) -> Bool {
        guard !path.isEmpty else { return true }
        var isDirectory: ObjCBool = false
        let exists = FileManager.default.fileExists(atPath: path, isDirectory: &isDirectory)
        return !(exists && isDirectory.boolValue)
    }

    /// The configured local vault path when its folder is gone.
    ///
    /// Nil when no local vault is configured, when a server connection is
    /// active (there is no local folder to check), or when the folder is
    /// present — so a genuinely empty but existing vault is unaffected.
    public static var missingLocalVaultPath: String? {
        guard !ServerConnectionConfig.hasConnection else { return nil }
        guard let path = vaultPath() else { return nil }
        return isVaultDirectoryMissing(path) ? path : nil
    }

    /// Save a chosen vault folder URL. Stores both a security-scoped bookmark
    /// (for sandbox persistence) and the plain string path.
    public static func setVault(url: URL) {
        ServerConnectionConfig.reset()
        let accessGranted = url.startAccessingSecurityScopedResource()
        defer {
            if accessGranted { url.stopAccessingSecurityScopedResource() }
        }
        saveBookmark(for: url)
        UserDefaults.standard.set(url.path, forKey: Key.vaultPath)
        UserDefaults.standard.set(false, forKey: Key.isDemoMode)
        registerActiveVault(name: nil, path: url.path, isDemoMode: false)
        if finderFavoritesEnabled {
            registerInFinderFavorites(url)
        }
    }

    /// Mark the current vault as demo mode and save the path.
    public static func setDemoVault(url: URL) {
        ServerConnectionConfig.reset()
        let accessGranted = url.startAccessingSecurityScopedResource()
        defer {
            if accessGranted { url.stopAccessingSecurityScopedResource() }
        }
        saveBookmark(for: url)
        UserDefaults.standard.set(url.path, forKey: Key.vaultPath)
        UserDefaults.standard.set(true, forKey: Key.isDemoMode)
        registerActiveVault(name: nil, path: url.path, isDemoMode: true)
        if finderFavoritesEnabled {
            registerInFinderFavorites(url)
        }
    }

    /// Activates a registry entry as the current vault: writes the legacy
    /// single-vault keys (path, bookmark, demo flag) from the entry so every
    /// existing `--vault`-based code path keeps working, records the entry as
    /// last opened, and clears any server connection.
    ///
    /// The registry entry itself is owned by `VaultRegistry`; this method only
    /// makes it the *active* vault (issue #296).
    @discardableResult
    public static func activate(_ entry: VaultEntry) -> VaultEntry {
        if entry.kind == .local {
            // Local and server mode exclude each other; activating a local
            // vault clears any server connection. A server entry never resets
            // the server config here — the server connection owns that mode.
            ServerConnectionConfig.reset()
            applyLocalKeys(entry, defaults: .standard)
        }
        let registry = VaultRegistry()
        return registry.recordOpened(entry.id) ?? entry
    }

    /// Writes the legacy single-vault keys from a local registry entry.
    /// Internal so tests can exercise the key mapping without touching the
    /// real UserDefaults or the Keychain.
    static func applyLocalKeys(_ entry: VaultEntry, defaults: UserDefaults) {
        if let bookmark = entry.bookmarkData {
            defaults.set(bookmark, forKey: Key.vaultBookmark)
        }
        if let path = entry.path {
            defaults.set(path, forKey: Key.vaultPath)
        }
        defaults.set(entry.isDemoMode, forKey: Key.isDemoMode)
    }

    /// Adds the currently configured local vault to the registry if it is not
    /// already present. Called by the onboarding flow after `setVault` /
    /// `setDemoVault` so the first vault becomes a named registry entry.
    private static func registerActiveVault(name: String?, path: String, isDemoMode: Bool) {
        let registry = VaultRegistry()
        let bookmark = UserDefaults.standard.data(forKey: Key.vaultBookmark)
        _ = registry.registerLocal(
            name: name ?? URL(fileURLWithPath: path).lastPathComponent,
            path: path,
            bookmarkData: bookmark,
            isDemoMode: isDemoMode
        )
    }

    /// Reset the vault configuration — used by Settings to re-enter onboarding.
    public static func reset() {
        if let path = vaultPath() {
            unregisterFromFinderFavorites(URL(fileURLWithPath: path))
        }
		resetLocalVault()
		ServerConnectionConfig.reset()
	}

	static func resetLocalVault() {
        UserDefaults.standard.removeObject(forKey: Key.vaultPath)
        UserDefaults.standard.removeObject(forKey: Key.vaultBookmark)
        UserDefaults.standard.removeObject(forKey: Key.isDemoMode)
    }

    // MARK: - Finder Favorites (opt-in, see issue #299)

    /// Whether the current vault should be kept registered in Finder's
    /// Favorites sidebar. Off by default — the user opts in explicitly.
    public static var finderFavoritesEnabled: Bool {
        get { UserDefaults.standard.bool(forKey: Key.finderFavoritesEnabled) }
        set {
            UserDefaults.standard.set(newValue, forKey: Key.finderFavoritesEnabled)
            guard let path = vaultPath() else { return }
            let url = URL(fileURLWithPath: path)
            if newValue {
                registerInFinderFavorites(url)
            } else {
                unregisterFromFinderFavorites(url)
            }
        }
    }

    /// Whether the current vault is present in Finder's Favorites sidebar
    /// right now. Used to detect a manual removal by the user so it is
    /// respected instead of silently re-adding the entry.
    public static func isVaultInFinderFavorites() -> Bool {
#if os(macOS)
        guard let path = vaultPath() else { return false }
        return FinderFavorites.isFolderInFavorites(URL(fileURLWithPath: path))
#else
        return false
#endif
    }

    /// Call once per launch after the vault is known. If the setting is
    /// enabled but the entry was removed manually from Finder since then,
    /// this respects that removal instead of re-adding it.
    public static func reconcileFinderFavoritesOnLaunch() {
        guard finderFavoritesEnabled, vaultPath() != nil else { return }
        if !isVaultInFinderFavorites() {
            UserDefaults.standard.set(false, forKey: Key.finderFavoritesEnabled)
        }
    }

    // MARK: - Security-Scoped Bookmarks

    private static func saveBookmark(for url: URL) {
        let accessGranted = url.startAccessingSecurityScopedResource()
        defer {
            if accessGranted { url.stopAccessingSecurityScopedResource() }
        }
        guard let data = try? url.bookmarkData(
            options: .withSecurityScope,
            includingResourceValuesForKeys: nil,
            relativeTo: nil
        ) else { return }
        UserDefaults.standard.set(data, forKey: Key.vaultBookmark)
    }

    /// Adds `url` to Finder's Favorites sidebar so the vault folder is
    /// always one click away.  This is a no‑op when the folder is already
    /// in the sidebar.
    static func registerInFinderFavorites(_ url: URL) {
#if os(macOS)
        FinderFavorites.addFolderToFavorites(url)
#endif
    }

    /// Removes `url` from Finder's Favorites sidebar.
    static func unregisterFromFinderFavorites(_ url: URL) {
#if os(macOS)
        FinderFavorites.removeFolderFromFavorites(url)
#endif
    }
}
