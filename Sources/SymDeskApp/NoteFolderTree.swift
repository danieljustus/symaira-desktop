import Foundation
import SymDeskCore

// MARK: - Folder Node Model

/// A node in the sidebar folder tree — either a folder (with children)
/// or a note leaf.
struct FolderNode: Identifiable, Hashable {
    let id: String
    let name: String
    let isFolder: Bool
    let note: Note?
    let children: [FolderNode]
    /// Set for leaf nodes that share their title with another note.
    /// Contains the parent folder name to help disambiguate.
    let containingFolder: String?

    func hash(into hasher: inout Hasher) {
        hasher.combine(id)
    }

    static func == (lhs: FolderNode, rhs: FolderNode) -> Bool {
        lhs.id == rhs.id
    }
}

/// Builds the sidebar folder tree from the notes as `symdesk ls --json`
/// reports them (issue #646): paths are vault-relative. Absolute paths are
/// tolerated and reduced to their vault-relative form before matching, so
/// every consumer sees the same tree regardless of the path form returned.
func buildFolderTree(from notes: [Note], vaultPath: String?) -> [FolderNode] {
    guard let vaultPath = vaultPath, !vaultPath.isEmpty else {
        return notes.map { FolderNode(id: $0.path, name: $0.title, isFolder: false, note: $0, children: [], containingFolder: nil) }
    }

    let normalizedVault = vaultPath.hasSuffix("/") ? vaultPath : vaultPath + "/"

    // Count duplicate titles so identically-named notes show their folder context
    let titleCounts = Dictionary(grouping: notes, by: \.title).mapValues(\.count)

    // Build a trie from relative note paths
    class TrieNode {
        var name: String
        var notes: [Note] = []
        var children: [String: TrieNode] = [:]
        init(name: String) { self.name = name }
    }

    let root = TrieNode(name: "")

    for note in notes {
        let relPath: String
        if note.path.hasPrefix(normalizedVault) {
            relPath = String(note.path.dropFirst(normalizedVault.count))
        } else if note.path.hasPrefix("/") {
            // Absolute path that does not sit under the vault prefix —
            // e.g. a differently-normalised vault spelling. Fall back to
            // matching on the vault's final path component.
            let vaultName = (vaultPath as NSString).lastPathComponent
            if let range = note.path.range(of: "/\(vaultName)/") {
                relPath = String(note.path[range.upperBound...])
            } else {
                continue
            }
        } else {
            // Already vault-relative — the exact shape `symdesk ls --json`
            // returns. Previously these notes were all filtered out, which
            // left the sidebar Notes section permanently empty (issue #646).
            relPath = note.path
        }

        var components = relPath.split(separator: "/").map(String.init)
        guard !components.isEmpty else { continue }
        components.removeLast() // strip the filename

        var current = root
        for component in components {
            if current.children[component] == nil {
                current.children[component] = TrieNode(name: component)
            }
            current = current.children[component]!
        }
        current.notes.append(note)
    }

    func convert(_ node: TrieNode) -> [FolderNode] {
        var result: [FolderNode] = []

        // Folders first, sorted alphabetically
        for key in node.children.keys.sorted(by: { $0.localizedCaseInsensitiveCompare($1) == .orderedAscending }) {
            let child = node.children[key]!
            let subChildren = convert(child)
            result.append(FolderNode(
                id: key,
                name: key,
                isFolder: true,
                note: nil,
                children: subChildren,
                containingFolder: nil
            ))
        }

        // Notes, sorted alphabetically by title
        for note in node.notes.sorted(by: { $0.title.localizedCaseInsensitiveCompare($1.title) == .orderedAscending }) {
            let hasDuplicates = (titleCounts[note.title] ?? 0) > 1
            let containingFolder: String?
            if hasDuplicates, !node.name.isEmpty {
                containingFolder = node.name
            } else if hasDuplicates {
                containingFolder = "Vault root"
            } else {
                containingFolder = nil
            }
            result.append(FolderNode(
                id: note.path,
                name: note.title,
                isFolder: false,
                note: note,
                children: [],
                containingFolder: containingFolder
            ))
        }

        return result
    }

    return convert(root)
}
