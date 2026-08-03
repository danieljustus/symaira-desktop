import CoreSpotlight
import Foundation

/// Indexes vault notes into iOS Core Spotlight so they are findable from
/// the home-screen search without opening the app first.
///
/// Each item carries the note's title, tags and a body excerpt plus a deep
/// link (`symdesk://open/<vault-relative path>`) that opens the note
/// directly. The index is owned by the system but *fed* by us, so content
/// is only as current as the last successful vault reload. `removeAll`
/// purges everything when the vault is disconnected or reset — content
/// must not stay searchable after the user revokes access.
///
/// CSSearchableIndex is not Sendable; the class is confined to its own
/// serial queue so calls from the store's background tasks are safe.
final class MobileSpotlightIndexer: @unchecked Sendable {
    private let index = CSSearchableIndex.default()
    private let domainIdentifier = "com.symaira.desktop.ios.vault"
    private let queue = DispatchQueue(label: "symdesk.mobile.spotlight")

    /// Replaces the whole Spotlight index with the given notes. Cheap for
    /// typical vault sizes (one batch) and always consistent — partial
    /// updates risk stale entries after renames or deletions.
    func replace(with notes: [MobileNote]) {
        let items = notes.map(Self.searchableItem)
        queue.async { [index, domainIdentifier] in
            index.deleteSearchableItems(withDomainIdentifiers: [domainIdentifier]) { _ in
                guard !items.isEmpty else { return }
                index.indexSearchableItems(items) { _ in }
            }
        }
    }

    func removeAll() {
        queue.async { [index, domainIdentifier] in
            index.deleteSearchableItems(withDomainIdentifiers: [domainIdentifier]) { _ in }
        }
    }

    /// Deep link that opens `note` directly in the app. Also used by
    /// Spotlight taps: the system launches the app with this URL.
    static func deepLink(for note: MobileNote) -> URL {
        deepLink(forPath: note.path)
    }

    static func deepLink(forPath path: String) -> URL {
        var components = URLComponents()
        components.scheme = "symdesk"
        components.host = "open"
        components.path = "/" + path
        return components.url ?? URL(string: "symdesk://open/")!
    }

    /// Parses a `symdesk://open/<path>` URL back into a vault-relative
    /// path, or nil when the URL is not an open link.
    static func path(from url: URL) -> String? {
        guard url.scheme == "symdesk", url.host == "open" else { return nil }
        let path = url.path
        guard path.count > 1 else { return nil }
        return String(path.dropFirst())
    }

    private static func searchableItem(for note: MobileNote) -> CSSearchableItem {
        let attributeSet = CSSearchableItemAttributeSet(contentType: .plainText)
        attributeSet.title = note.title
        attributeSet.contentDescription = excerpt(of: note)
        attributeSet.keywords = note.tags
        attributeSet.relatedUniqueIdentifier = note.path
        attributeSet.contentURL = deepLink(for: note)

        let item = CSSearchableItem(
            uniqueIdentifier: note.path,
            domainIdentifier: "com.symaira.desktop.ios.vault",
            attributeSet: attributeSet
        )
        return item
    }

    private static func excerpt(of note: MobileNote) -> String {
        let body = note.body.replacingOccurrences(of: "\n", with: " ").trimmingCharacters(in: .whitespaces)
        if body.isEmpty { return note.path }
        return body.count > 160 ? String(body.prefix(160)) + "…" : body
    }
}
