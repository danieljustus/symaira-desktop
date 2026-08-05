import Foundation

/// Drains the share-extension inbox inside the App Group container.
/// Shared files land here by the extension even when the main app is not
/// running; on the next launch (or foreground) this moves each item into
/// the write layer as an uploadOriginal, then removes the inbox copy.
final class MobileShareInbox: @unchecked Sendable {
    static let appGroupID = "group.com.symaira.desktop.ios"

    private let fileManager: FileManager
    private let coordinator: MobileWriteCoordinator
    /// Test hook: explicit inbox directory instead of the App Group
    /// container (unavailable in unit tests).
    private let inboxOverride: URL?

    init(fileManager: FileManager = .default, coordinator: MobileWriteCoordinator, inboxURL: URL? = nil) {
        self.fileManager = fileManager
        self.coordinator = coordinator
        self.inboxOverride = inboxURL
    }

    /// The shared inbox directory; nil when the App Group is unavailable
    /// (e.g. running without the proper provisioning profile).
    var inboxURL: URL? {
        if let inboxOverride {
            return inboxOverride
        }
        guard let container = fileManager.containerURL(forSecurityApplicationGroupIdentifier: Self.appGroupID) else {
            return nil
        }
        return container.appendingPathComponent("ShareInbox", isDirectory: true)
    }

    /// Number of items waiting to be filed.
    func pendingCount() -> Int {
        (try? fileManager.contentsOfDirectory(atPath: inboxURL?.path ?? ""))?.count ?? 0
    }

    /// Moves every inbox item into the write layer. Items are appended in
    /// name order (oldest first) and the inbox copy is removed only after
    /// a successful enqueue, so nothing is lost on failure.
    func drain() async {
        guard let inbox = inboxURL else { return }
        let names = (try? fileManager.contentsOfDirectory(atPath: inbox.path))?.sorted() ?? []
        for name in names {
            let source = inbox.appendingPathComponent(name)
            guard let payload = try? Data(contentsOf: source) else { continue }
            let filename = Self.filename(from: name)
            do {
                try await coordinator.enqueue(MobileOutboxEntry(
                    kind: .uploadOriginal,
                    path: filename,
                    originalData: payload,
                    originalFilename: filename,
                    folder: nil
                ))
                try? fileManager.removeItem(at: source)
            } catch {
                // Leave the file; a later drain will retry.
            }
        }
    }

    /// Normalises share filenames so the ingest pipeline can classify
    /// them: keep a sensible base name, always add an extension the
    /// watcher understands.
    static func filename(from name: String) -> String {
        let cleaned = name
            .replacingOccurrences(of: "share-", with: "")
            .replacingOccurrences(of: " ", with: "_")
        return cleaned
    }
}
