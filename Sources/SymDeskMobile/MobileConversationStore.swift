import Foundation

/// One chat message with its citations. Persisted locally and deletable
/// per conversation (#330).
struct MobileChatMessage: Codable, Identifiable, Equatable, Sendable {
    let id: UUID
    var role: Role
    var text: String
    var citations: [MobileChatCitation]
    var createdAt: Date
    /// Context note path when the question was asked about an open note.
    var contextPath: String?

    enum Role: String, Codable, Sendable {
        case user
        case assistant
    }

    init(
        id: UUID = UUID(),
        role: Role,
        text: String,
        citations: [MobileChatCitation] = [],
        createdAt: Date = Date(),
        contextPath: String? = nil
    ) {
        self.id = id
        self.role = role
        self.text = text
        self.citations = citations
        self.createdAt = createdAt
        self.contextPath = contextPath
    }
}

/// A citation the assistant attached to an answer: a vault-relative path
/// the user can tap to open the note/document inside the app.
struct MobileChatCitation: Codable, Identifiable, Equatable, Sendable {
    let path: String
    let title: String
    let snippet: String
    let score: Double

    var id: String { path }
}

/// A persisted conversation: messages plus metadata. Deletable as a whole.
struct MobileConversation: Codable, Identifiable, Equatable, Sendable {
    let id: UUID
    var title: String
    var messages: [MobileChatMessage]
    var updatedAt: Date

    var idValue: String { id.uuidString }

    init(id: UUID = UUID(), title: String, messages: [MobileChatMessage] = [], updatedAt: Date = Date()) {
        self.id = id
        self.title = title
        self.messages = messages
        self.updatedAt = updatedAt
    }
}

/// Persists conversations on disk so they survive launches; deleting a
/// conversation removes its file. One JSON file per conversation.
actor MobileConversationStore {
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
            self.directory = base.appendingPathComponent("SymDeskMobile/Chats", isDirectory: true)
        }
        self.encoder = JSONEncoder()
        self.encoder.dateEncodingStrategy = .iso8601
        self.encoder.outputFormatting = [.sortedKeys]
        self.decoder = JSONDecoder()
        self.decoder.dateDecodingStrategy = .iso8601
        try FileManager.default.createDirectory(at: self.directory, withIntermediateDirectories: true)
    }

    func save(_ conversation: MobileConversation) throws {
        let data = try encoder.encode(conversation)
        try data.write(to: fileURL(for: conversation.id), options: .atomic)
    }

    func load(id: UUID) -> MobileConversation? {
        guard let data = try? Data(contentsOf: fileURL(for: id)) else { return nil }
        return try? decoder.decode(MobileConversation.self, from: data)
    }

    /// All conversations, newest-updated first.
    func all() -> [MobileConversation] {
        let urls = (try? FileManager.default.contentsOfDirectory(
            at: directory,
            includingPropertiesForKeys: [.contentModificationDateKey],
            options: [.skipsHiddenFiles]
        )) ?? []
        return urls
            .filter { $0.pathExtension == "json" }
            .compactMap { url in
                guard let data = try? Data(contentsOf: url) else { return nil }
                return try? decoder.decode(MobileConversation.self, from: data)
            }
            .sorted { $0.updatedAt > $1.updatedAt }
    }

    func delete(id: UUID) throws {
        try? FileManager.default.removeItem(at: fileURL(for: id))
    }

    func deleteAll() throws {
        let urls = (try? FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)) ?? []
        for url in urls where url.pathExtension == "json" {
            try? FileManager.default.removeItem(at: url)
        }
    }

    private func fileURL(for id: UUID) -> URL {
        directory.appendingPathComponent(id.uuidString + ".json")
    }
}
