import Foundation
#if os(macOS)
@preconcurrency import CoreServices

/// Helper to register a folder in Finder's Favorites sidebar.
///
/// Uses the Launch Services shared file list API (`LSSharedFileList`) to add
/// the vault folder to the Finder sidebar so users can reach it directly
/// without navigating manually each time.  Duplicates are automatically
/// avoided by checking resolved URLs before inserting.
public enum FinderFavorites {

    /// Adds `folderURL` to the Finder sidebar Favorites, unless it is already
    /// present.  This is a no‑op on non‑macOS platforms.
    public static func addFolderToFavorites(_ folderURL: URL) {
        let path = folderURL.standardizedFileURL.path

        let favoriteItemListID = kLSSharedFileListFavoriteItems.takeUnretainedValue()
        let itemLast = kLSSharedFileListItemLast.takeUnretainedValue()

        guard let favoritesList = LSSharedFileListCreate(
            kCFAllocatorDefault,
            favoriteItemListID,
            nil
        )?.takeRetainedValue() else {
            return
        }

        // Check for existing entry to avoid duplicates.
        var seed: UInt32 = 0
        if let snapshot = LSSharedFileListCopySnapshot(favoritesList, &seed)?.takeRetainedValue() {
            let snapshotArray = snapshot as [AnyObject]
            for case let item as LSSharedFileListItem in snapshotArray {
                guard let resolved = LSSharedFileListItemCopyResolvedURL(
                    item,
                    UInt32(0),  // no resolution flags
                    nil
                )?.takeRetainedValue() else { continue }
                if (resolved as URL).standardizedFileURL.path == path {
                    return // Already in favorites
                }
            }
        }

        // Insert at the end of the favorites list.
        let displayName = folderURL.lastPathComponent as CFString
        LSSharedFileListInsertItemURL(
            favoritesList,
            itemLast,
            displayName,
            nil,
            folderURL as CFURL,
            CFDictionaryCreate(kCFAllocatorDefault, nil, nil, 0, nil, nil),
            nil
        )
    }
}
#endif
