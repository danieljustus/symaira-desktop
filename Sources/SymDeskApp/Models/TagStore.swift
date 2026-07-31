import Foundation
import SymDeskCore

/// Efficiently aggregates tags across the vault by scanning file frontmatter.
/// For local vaults, reads the first few kilobytes directly from disk (fast).
/// For remote vaults, batch-fetches note content through the core.
enum TagStore {

    /// Aggregate tags from the given notes by scanning their frontmatter.
    /// Uses the vault path to resolve absolute file paths for local vaults.
    /// Returns tags sorted by count descending, then alphabetically.
    static func aggregate(from notes: [Note], vaultPath: String?, isRemote: Bool, core: DeskCore) async throws -> [TagEntry] {
        if isRemote {
            return try await aggregateRemote(notes: notes, core: core)
        } else {
            return try await aggregateLocal(notes: notes, vaultPath: vaultPath)
        }
    }

    /// Aggregate tags from local files by reading frontmatter directly from disk.
    private static func aggregateLocal(notes: [Note], vaultPath: String?) async throws -> [TagEntry] {
        var counts: [String: Int] = [:]

        try await withThrowingTaskGroup(of: (String, [String]).self) { group in
            for note in notes {
                group.addTask {
                    let tags = readTagsFromFile(path: note.path, vaultPath: vaultPath)
                    return (note.path, tags)
                }
            }

            for try await (_, tags) in group {
                for tag in tags {
                    let normalized = tag.lowercased().trimmingCharacters(in: .whitespaces)
                    guard !normalized.isEmpty else { continue }
                    counts[normalized, default: 0] += 1
                }
            }
        }

        return counts
            .map { TagEntry(name: $0.key, count: $0.value) }
            .sorted { $0.count == $1.count ? $0.name < $1.name : $0.count > $1.count }
    }

    /// Aggregate tags for remote vaults by fetching content via API.
    private static func aggregateRemote(notes: [Note], core: DeskCore) async throws -> [TagEntry] {
        var counts: [String: Int] = [:]

        try await withThrowingTaskGroup(of: (String, [String]).self) { group in
            for note in notes {
                group.addTask {
                    let content = try await core.docNoteContent(path: note.path)
                    let tags = TagParser.extractTags(from: content)
                    return (note.path, tags)
                }
            }

            for try await (_, tags) in group {
                for tag in tags {
                    let normalized = tag.lowercased().trimmingCharacters(in: .whitespaces)
                    guard !normalized.isEmpty else { continue }
                    counts[normalized, default: 0] += 1
                }
            }
        }

        return counts
            .map { TagEntry(name: $0.key, count: $0.value) }
            .sorted { $0.count == $1.count ? $0.name < $1.name : $0.count > $1.count }
    }

    /// Read tags from a local file by scanning only the frontmatter block.
    private static func readTagsFromFile(path: String, vaultPath: String?) -> [String] {
        guard let absolutePath = DocumentPreviewResolver.noteURL(documentPath: path, vaultPath: vaultPath)?.path else {
            return []
        }

        guard let fileHandle = try? FileHandle(forReadingFrom: URL(fileURLWithPath: absolutePath)) else {
            return []
        }
        defer { try? fileHandle.close() }

        // Read first 8KB — enough for any practical frontmatter block
        let data = fileHandle.readData(ofLength: 8192)
        guard let chunk = String(data: data, encoding: .utf8) else {
            return []
        }

        return TagParser.extractTags(from: chunk)
    }
}
