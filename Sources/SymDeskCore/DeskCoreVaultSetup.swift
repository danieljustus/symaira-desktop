import Foundation
import SymairaCLIRunner

extension DeskCore {
    /// Load vault path from VaultConfig on app launch.
    public func loadVaultFromConfig() {
        // Prefer the registry's most recently opened entry so a relaunch
        // reopens the last active vault (issue #296). Falls back to the
        // legacy single-vault keys, which the registry migrates on first use.
        // A live server connection is never displaced: it owns the active mode.
        if ServerConnectionConfig.connection() == nil,
           let entry = VaultRegistry().mostRecentlyOpened() {
            VaultConfig.activate(entry)
        }
        if let path = VaultConfig.vaultPath() {
            self.vaultPath = path
            // Register in Finder's Favorites sidebar so vaults configured
            // before this feature existed get picked up (see issue #299).
            let vaultURL = URL(fileURLWithPath: path)
            VaultConfig.registerInFinderFavorites(vaultURL)
        }
    }

    /// Switches the active vault to a registered local entry: activates the
    /// entry's path/bookmark, tears down the event watcher and re-points every
    /// `--vault` invocation by publishing the new `vaultPath`.
    ///
    /// The caller is responsible for restarting the event watcher and
    /// reloading UI state; a `.vaultSwitched` notification is posted so the
    /// app shell can react in one place.
    public func switchVault(to entry: VaultEntry) {
        guard entry.kind == .local, let path = entry.path else { return }
        VaultConfig.activate(entry)
        vaultPath = path
        serverURL = nil
        NotificationCenter.default.post(name: .vaultSwitched, object: nil)
    }

    /// Creates a new vault folder with the contract scaffold (templates/ and
    /// assets/ directories, see VAULT.md), runs the first index so the sidecar
    /// DB exists, registers the vault in the registry and activates it.
    /// Returns the created entry.
    public func createVault(named name: String, at url: URL) async throws -> VaultEntry {
        let fileManager = FileManager.default
        if !fileManager.fileExists(atPath: url.path) {
            try fileManager.createDirectory(at: url, withIntermediateDirectories: true)
        }
        // Contract scaffold: templates/ for note templates, assets/ for
        // pasted/dropped images. Both are referenced by the service layer.
        try fileManager.createDirectory(
            at: url.appendingPathComponent("templates", isDirectory: true),
            withIntermediateDirectories: true
        )
        try fileManager.createDirectory(
            at: url.appendingPathComponent(VaultAssets.defaultFolderName, isDirectory: true),
            withIntermediateDirectories: true
        )

        _ = try await indexVault(path: url.path)

        let bookmarkData = try? url.bookmarkData(
            options: .withSecurityScope,
            includingResourceValuesForKeys: nil,
            relativeTo: nil
        )
        let entry = VaultRegistry().registerLocal(
            name: name,
            path: url.path,
            bookmarkData: bookmarkData
        )
        VaultConfig.activate(entry)
        vaultPath = url.path
        serverURL = nil
        NotificationCenter.default.post(name: .vaultSwitched, object: nil)
        return entry
    }

    /// Removes a vault from the registry list. The folder on disk and the
    /// sidecar DB are left untouched (issue #296). When the removed entry is
    /// the currently active vault, the active association is cleared so the
    /// removed vault does not silently resurrect on the next relaunch.
    public func removeVaultFromRegistry(id: UUID) {
        let registry = VaultRegistry()
        let removed = registry.entry(id: id)
        registry.remove(id: id)
        if let removed, removed.kind == .local, let path = removed.path,
           path == VaultConfig.vaultPath() {
            VaultConfig.resetLocalVault()
            vaultPath = nil
            NotificationCenter.default.post(name: .vaultReset, object: nil)
        }
    }

    public func indexVault(path: String) async throws -> String {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        let data = try await runner.runChecked(
            tool.location.url,
            arguments: ["index", "--json", "--vault", path]
        )
        struct IndexResult: Codable, Sendable {
            let status: String
            let indexed: Int
            let skipped: Int
        }
        let result = try JSONDecoder().decode(IndexResult.self, from: data)
        return "Index complete. \(result.indexed) new/updated files, \(result.skipped) skipped."
    }

    public func initDemo(into demoDir: String) async throws -> String {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        let data = try await runner.runChecked(
            tool.location.url,
            arguments: ["demo", "init", "--json", demoDir]
        )
        struct DemoInitResult: Codable, Sendable {
            let status: String
            let path: String
        }
        let result = try JSONDecoder().decode(DemoInitResult.self, from: data)
        return result.path
    }
}
