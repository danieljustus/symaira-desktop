import Foundation

/// Drains the share-extension inbox inside the App Group container.
/// Shared files land here by the extension even when the main app is not
/// running; on the next launch (or foreground) this moves each item into
/// the write layer and removes the inbox copy. URL/text shares become
/// note creations (contract-v2, with the URL recorded as `source` in the
/// frontmatter, per #327); PDFs, images and other originals keep the
/// ingest path (`uploadOriginal`).
final class MobileShareInbox: @unchecked Sendable {
    static let appGroupID = "group.com.symaira.desktop.ios"

    /// Descriptor prefixes the share extension writes for URL/text shares.
    /// Anything else in the inbox is an original (PDF/image/file).
    private static let urlPrefix = "url: "
    private static let textPrefix = "text: "
    private static let commentPrefix = "comment: "

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

    /// Moves every inbox item into the write layer. URL/text descriptors
    /// are routed into the note-create path; everything else is enqueued
    /// as an `uploadOriginal`. Items are appended in name order (oldest
    /// first) and the inbox copy is removed only after a successful
    /// enqueue, so nothing is lost on failure.
    func drain() async {
        guard let inbox = inboxURL else { return }
        let names = (try? fileManager.contentsOfDirectory(atPath: inbox.path))?.sorted() ?? []
        for name in names {
            let source = inbox.appendingPathComponent(name)
            guard let payload = try? Data(contentsOf: source) else { continue }
            let filename = Self.filename(from: name)
            do {
                if let note = Self.noteShare(from: payload) {
                    try await coordinator.enqueue(MobileOutboxEntry(
                        kind: .createNote,
                        path: MobileNoteWriter.filename(for: note.title),
                        content: MobileNoteWriter.noteDocument(
                            title: note.title,
                            body: note.body,
                            source: note.source
                        )
                    ))
                } else {
                    try await coordinator.enqueue(MobileOutboxEntry(
                        kind: .uploadOriginal,
                        path: filename,
                        originalData: payload,
                        originalFilename: filename,
                        folder: nil
                    ))
                }
                try? fileManager.removeItem(at: source)
            } catch {
                // Leave the file; a later drain will retry.
            }
        }
    }

    /// Removes every queued share item. Called when the vault is
    /// disconnected so shares filed while disconnected can never drain
    /// into another vault (cross-vault leak, #327 AC5).
    func purge() {
        guard let inbox = inboxURL else { return }
        let names = (try? fileManager.contentsOfDirectory(atPath: inbox.path)) ?? []
        for name in names {
            try? fileManager.removeItem(at: inbox.appendingPathComponent(name))
        }
    }

    // MARK: - Descriptor decoding

    /// Decodes a share-inbox item into a note-creation payload, or nil
    /// when the item is an original that keeps the ingest path.
    ///
    /// The share extension writes URL/text shares as line-based
    /// descriptors: `url: <url>` / `text: <text>`, with an optional
    /// `comment: <comment>` first line. The comment is appended to the
    /// note body. Plain files (PDFs, images…) are binary and fall through
    /// to the ingest path.
    static func noteShare(from payload: Data) -> (title: String, body: String, source: String?)? {
        guard let text = String(data: payload, encoding: .utf8) else { return nil }
        var lines = text.components(separatedBy: "\n")

        var comment = ""
        if let first = lines.first, first.hasPrefix(Self.commentPrefix) {
            comment = String(first.dropFirst(Self.commentPrefix.count))
                .trimmingCharacters(in: .whitespacesAndNewlines)
            lines.removeFirst()
        }
        guard let head = lines.first else { return nil }

        if head.hasPrefix(Self.urlPrefix) {
            let url = String(head.dropFirst(Self.urlPrefix.count))
                .trimmingCharacters(in: .whitespacesAndNewlines)
            guard !url.isEmpty else { return nil }
            var body = url
            if !comment.isEmpty { body += "\n\n" + comment }
            return (Self.title(forURL: url), body, url)
        }

        if head.hasPrefix(Self.textPrefix) {
            let firstPart = String(head.dropFirst(Self.textPrefix.count))
            let rest = lines.dropFirst().joined(separator: "\n")
            let body = (rest.isEmpty ? firstPart : firstPart + "\n" + rest)
                .trimmingCharacters(in: .whitespacesAndNewlines)
            guard !body.isEmpty else { return nil }
            var noteBody = body
            if !comment.isEmpty { noteBody += "\n\n" + comment }
            return (Self.title(forText: body), noteBody, nil)
        }

        return nil
    }

    /// Note title for a URL share: host plus URL path, so repeated shares
    /// from one site land as distinct notes ("example.com/a").
    private static func title(forURL url: String) -> String {
        guard let parsed = URL(string: url), let host = parsed.host, !host.isEmpty else {
            return url
        }
        let path = parsed.pathComponents.filter { $0 != "/" }.joined(separator: "/")
        return path.isEmpty ? host : host + "/" + path
    }

    /// Note title for a text share: the first line, trimmed to a
    /// reasonable length so the filename stays readable.
    private static func title(forText text: String) -> String {
        let firstLine = text
            .split(separator: "\n", maxSplits: 1, omittingEmptySubsequences: false)
            .first
            .map(String.init) ?? text
        return String(firstLine.trimmingCharacters(in: .whitespacesAndNewlines).prefix(60))
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
