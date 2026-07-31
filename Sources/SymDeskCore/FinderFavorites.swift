import Foundation
#if os(macOS)
@preconcurrency import CoreServices

/// Helper to register or remove a folder in Finder's Favorites sidebar.
///
/// Uses the Launch Services shared file list API (`LSSharedFileList`) to add
/// the vault folder to the Finder sidebar so users can reach it directly
/// without navigating manually each time. The API is formally deprecated but
/// still functions; the deprecation warnings are suppressed only in this
/// file, and the sentinel constants `kLSSharedFileListItemBeforeFirst` /
/// `kLSSharedFileListItemLast` are never used directly — passing either into
/// `LSSharedFileListInsertItemURL` from Swift makes ARC retain the sentinel
/// pointer and crashes the process. Insertion always anchors on a real item
/// from the current snapshot instead.
public enum FinderFavorites {

    /// Returns the resolved `LSSharedFileListItem` matching `folderURL`, if any.
    private static func existingItem(
        in list: LSSharedFileList,
        matching folderURL: URL
    ) -> LSSharedFileListItem? {
        let path = folderURL.standardizedFileURL.path
        var seed: UInt32 = 0
        guard let snapshot = LSSharedFileListCopySnapshot(list, &seed)?.takeRetainedValue() else {
            return nil
        }
        for case let item as LSSharedFileListItem in snapshot as [AnyObject] {
            guard let resolved = LSSharedFileListItemCopyResolvedURL(item, 0, nil)?.takeRetainedValue() else {
                continue
            }
            if (resolved as URL).standardizedFileURL.path == path {
                return item
            }
        }
        return nil
    }

    private static func favoritesList() -> LSSharedFileList? {
        let favoriteItemListID = kLSSharedFileListFavoriteItems.takeUnretainedValue()
        return LSSharedFileListCreate(kCFAllocatorDefault, favoriteItemListID, nil)?.takeRetainedValue()
    }

    /// Whether `folderURL` is currently present in the Finder Favorites sidebar.
    public static func isFolderInFavorites(_ folderURL: URL) -> Bool {
        guard let list = favoritesList() else { return false }
        return existingItem(in: list, matching: folderURL) != nil
    }

    /// Adds `folderURL` to the Finder sidebar Favorites, unless it is already
    /// present. This is a no-op on non-macOS platforms.
    public static func addFolderToFavorites(_ folderURL: URL) {
        guard let list = favoritesList() else { return }
        if existingItem(in: list, matching: folderURL) != nil {
            return // Already in favorites.
        }

        var seed: UInt32 = 0
        let snapshot = LSSharedFileListCopySnapshot(list, &seed)?.takeRetainedValue() as? [LSSharedFileListItem]
        // Anchor on the last real item in the current snapshot — never on the
        // kLSSharedFileListItemBeforeFirst/Last sentinel pointers, which crash
        // when Swift's ARC tries to retain them.
        guard let anchor = snapshot?.last else {
            return
        }

        let displayName = folderURL.lastPathComponent as CFString
        LSSharedFileListInsertItemURL(
            list,
            anchor,
            displayName,
            nil,
            folderURL as CFURL,
            nil,
            nil
        )
    }

    /// Removes `folderURL` from the Finder sidebar Favorites if present.
    /// This is a no-op on non-macOS platforms and when the folder is absent.
    public static func removeFolderFromFavorites(_ folderURL: URL) {
        guard let list = favoritesList(),
              let item = existingItem(in: list, matching: folderURL) else {
            return
        }
        LSSharedFileListItemRemove(list, item)
    }
}
#endif
