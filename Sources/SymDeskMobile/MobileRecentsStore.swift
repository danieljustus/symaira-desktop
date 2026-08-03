import Foundation

/// Recently-opened notes, persisted in the shared App Group container so
/// the home-screen widget can show them. Stored as JSON items (path +
/// title); the in-app list resolves items to full notes by path.
///
/// This replaces the previous standard-UserDefaults path array (#323);
/// the move to the shared container is part of the one-container decision
/// for share extension + widget (#327/#328 contract).
enum MobileRecentsStore {
    /// Shared App Group container. `var` so tests can isolate the suite.
    nonisolated(unsafe) static var suiteName = "group.com.symaira.desktop.ios"
    static let changedNotification = Notification.Name("symdesk.mobile.recents-changed")
    static let maxItems = 10
    private static let key = "symdesk.mobile.recently-opened.v1"

    struct RecentItem: Codable, Identifiable, Sendable, Equatable {
        let path: String
        let title: String

        var id: String { path }
    }

    static func defaults() -> UserDefaults {
        UserDefaults(suiteName: suiteName) ?? .standard
    }

    /// Current recents, newest first. Falls back to the legacy
    /// standard-UserDefaults path array on first launch after the move.
    static func read() -> [RecentItem] {
        if let data = defaults().data(forKey: key),
           let items = try? JSONDecoder().decode([RecentItem].self, from: data) {
            return items
        }
        // Legacy migration: the pre-#328 array stored paths only.
        let legacyPaths = UserDefaults.standard.stringArray(forKey: key) ?? []
        return legacyPaths.map { RecentItem(path: $0, title: (($0 as NSString).lastPathComponent as NSString).deletingPathExtension) }
    }

    /// Records an opened note at the front, deduplicating by path.
    static func record(path: String, title: String) {
        var items = read().filter { $0.path != path }
        items.insert(RecentItem(path: path, title: title), at: 0)
        if items.count > maxItems {
            items = Array(items.prefix(maxItems))
        }
        if let data = try? JSONEncoder().encode(items) {
            defaults().set(data, forKey: key)
        }
        NotificationCenter.default.post(name: changedNotification, object: nil)
    }

    /// Removes every entry (used on vault reset/disconnect so no stale
    /// items survive into the widget or the recents section).
    static func clear() {
        defaults().removeObject(forKey: key)
        NotificationCenter.default.post(name: changedNotification, object: nil)
    }
}
