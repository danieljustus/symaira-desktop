import Foundation

/// Persists composer drafts on disk so text survives backgrounding and
/// force-quitting before the user explicitly finishes the note. One JSON
/// file per draft under Application Support; drafts are cleared once they
/// are enqueued through the write layer.
actor MobileDraftStore {
    struct Draft: Codable, Identifiable, Equatable, Sendable {
        var id: String
        var title: String
        var body: String
        /// Vault-relative path when editing an existing note; nil for a
        /// new note. The raw source (including frontmatter) is kept
        /// verbatim so nothing the mobile editor does not render is lost.
        var existingPath: String?
        /// Optional target folder (vault-relative) for new notes.
        var folder: String?
        var updatedAt: Date

        var idValue: String { id }
    }

    private let directory: URL
    private let encoder: JSONEncoder
    private let decoder: JSONDecoder

    init(directory: URL? = nil) throws {
        if let directory {
            self.directory = directory
        } else {
            let base = try FileManager.default.url(
                for: .applicationSupportDirectory,
                in: .userDomainMask,
                appropriateFor: nil,
                create: true
            )
            self.directory = base.appendingPathComponent("SymDeskMobile/Drafts", isDirectory: true)
        }
        self.encoder = JSONEncoder()
        self.encoder.dateEncodingStrategy = .iso8601
        self.encoder.outputFormatting = [.sortedKeys]
        self.decoder = JSONDecoder()
        self.decoder.dateDecodingStrategy = .iso8601
        try FileManager.default.createDirectory(at: self.directory, withIntermediateDirectories: true)
    }

    func save(_ draft: Draft) throws {
        let data = try encoder.encode(draft)
        try data.write(to: fileURL(for: draft.id), options: .atomic)
    }

    func load(id: String) -> Draft? {
        guard let data = try? Data(contentsOf: fileURL(for: id)) else { return nil }
        return try? decoder.decode(Draft.self, from: data)
    }

    func all() -> [Draft] {
        let urls = (try? FileManager.default.contentsOfDirectory(
            at: directory,
            includingPropertiesForKeys: [.contentModificationDateKey],
            options: [.skipsHiddenFiles]
        )) ?? []
        return urls
            .filter { $0.pathExtension == "json" }
            .compactMap { url in
                guard let data = try? Data(contentsOf: url) else { return nil }
                return try? decoder.decode(Draft.self, from: data)
            }
            .sorted { $0.updatedAt > $1.updatedAt }
    }

    func delete(id: String) throws {
        try? FileManager.default.removeItem(at: fileURL(for: id))
    }

    private func fileURL(for id: String) -> URL {
        directory.appendingPathComponent(id + ".json")
    }
}
