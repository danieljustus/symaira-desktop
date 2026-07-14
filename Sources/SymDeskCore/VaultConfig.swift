import Foundation

/// Manages the vault location configuration for SymDesk.
///
/// Stores the chosen vault path in UserDefaults along with a security-scoped
/// bookmark so the path persists across app restarts even under sandboxing.
/// Every CLI invocation appends `--vault <path>` when a vault is configured.
public struct VaultConfig {

    // MARK: - UserDefaults Keys

    private enum Key {
        static let vaultPath = "symdesk.vaultPath"
        static let vaultBookmark = "symdesk.vaultBookmark"
        static let isDemoMode = "symdesk.isDemoMode"
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
    }

    /// Reset the vault configuration — used by Settings to re-enter onboarding.
    public static func reset() {
		resetLocalVault()
		ServerConnectionConfig.reset()
	}

	static func resetLocalVault() {
        UserDefaults.standard.removeObject(forKey: Key.vaultPath)
        UserDefaults.standard.removeObject(forKey: Key.vaultBookmark)
        UserDefaults.standard.removeObject(forKey: Key.isDemoMode)
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
}
