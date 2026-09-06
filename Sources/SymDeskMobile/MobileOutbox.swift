import Foundation

/// The kinds of write the outbox can carry. Every capture feature on iOS
/// (composer, scanner, Share Extension, inline AI) funnels through these,
/// so there is exactly one conflict-handling path.
enum MobileWriteKind: String, Codable, Sendable {
    case createNote
    case updateNote
    case uploadOriginal
}

/// Lifecycle of one outbox entry as shown in the UI.
enum MobileOutboxState: String, Codable, Sendable {
    /// Waiting to be applied (or waiting out its backoff window).
    case queued
    /// Currently being applied.
    case uploading
    /// Permanently rejected (permission, precondition conflict, invalid
    /// content). The entry stays visible with its reason until the user
    /// retries or removes it.
    case failed
}

/// Precondition captured when the user made the change. The adapter
/// re-checks it immediately before applying, so a remote change made while
/// the phone was offline is detected instead of silently overwritten.
///
/// Server mode uses an ETag (SHA-256 of the content the phone last saw,
/// computed from the parsed snapshot). Files mode uses modification date
/// plus size, because iCloud Drive offers no atomic compare-and-swap.
struct MobileWritePrecondition: Codable, Sendable {
    var etag: String?
    var modifiedAt: Date?
    var size: Int?

    static let none = MobileWritePrecondition()

    /// A precondition for a fresh create: the target must not exist.
    static let absent = MobileWritePrecondition()
}

/// One durable queued write. Codable so the whole queue survives app
/// termination; `originalData` is persisted as a sibling payload file
/// (see `MobileOutbox`), not inside this JSON.
struct MobileOutboxEntry: Identifiable, Codable, Sendable {
    let id: UUID
    var kind: MobileWriteKind
    /// Vault-relative target path (notes) or the destination file name
    /// inside the consume folder (uploads in Files mode).
    var path: String
    /// Full contract-v6-compatible Markdown for note operations.
    var content: String?
    /// Original bytes for `uploadOriginal`.
    var originalData: Data?
    var originalFilename: String?
    /// Optional note-folder override (composer "target folder").
    var folder: String?
    var createdAt: Date
    var attempts: Int
    var nextRetryAt: Date?
    var lastError: String?
    var state: MobileOutboxState
    var precondition: MobileWritePrecondition
    /// Set when the entry was resolved as a conflict: the local version was
    /// preserved at this vault-relative path and the remote version won.
    var conflictPath: String?

    init(
        kind: MobileWriteKind,
        path: String,
        content: String? = nil,
        originalData: Data? = nil,
        originalFilename: String? = nil,
        folder: String? = nil,
        precondition: MobileWritePrecondition = .none,
        createdAt: Date = Date()
    ) {
        self.id = UUID()
        self.kind = kind
        self.path = path
        self.content = content
        self.originalData = originalData
        self.originalFilename = originalFilename
        self.folder = folder
        self.createdAt = createdAt
        self.attempts = 0
        self.nextRetryAt = nil
        self.lastError = nil
        self.state = .queued
        self.precondition = precondition
        self.conflictPath = nil
    }
}

/// Errors the adapters raise while applying an entry. The coordinator maps
/// these onto queue states: `rejected` becomes a visible failed entry,
/// `network` is retried with backoff, `conflict` preserves the local
/// version as a sibling file and then surfaces as a failed entry with the
/// preserved path.
enum MobileWriteError: LocalizedError {
    case rejected(status: Int, reason: String)
    case network(underlying: String)
    case conflict(preservedAt: String, reason: String)
    case invalidContent(reason: String)

    var errorDescription: String? {
        switch self {
        case .rejected(let status, let reason):
            return "Server rejected the write (\(status)): \(reason)"
        case .network(let underlying):
            return "No connection: \(underlying)"
        case .conflict(let preservedAt, let reason):
            return "\(reason) — your version was kept as “\(preservedAt)”."
        case .invalidContent(let reason):
            return "Invalid content: \(reason)"
        }
    }
}

/// Persists queued writes on disk so they survive force-quitting the app.
///
/// The queue is a JSON document plus one payload file per upload entry
/// (payloads can be multi-megabyte scans; keeping them out of the JSON
/// keeps enqueue/dequeue cheap and the metadata readable). All mutations
/// are serialized through the actor and written atomically.
actor MobileOutbox {
    private let directory: URL
    private let metadataURL: URL
    private let payloadsURL: URL
    private var entries: [MobileOutboxEntry] = []
    private let encoder: JSONEncoder
    private let decoder: JSONDecoder

    init(directory: URL) throws {
        self.directory = directory
        self.metadataURL = directory.appendingPathComponent("outbox.json")
        self.payloadsURL = directory.appendingPathComponent("payloads")
        self.encoder = JSONEncoder()
        self.encoder.dateEncodingStrategy = .iso8601
        self.encoder.outputFormatting = [.sortedKeys]
        self.decoder = JSONDecoder()
        self.decoder.dateDecodingStrategy = .iso8601

        try FileManager.default.createDirectory(
            at: directory,
            withIntermediateDirectories: true
        )
        try FileManager.default.createDirectory(
            at: payloadsURL,
            withIntermediateDirectories: true
        )
        entries = Self.loadMetadata(from: metadataURL, decoder: decoder)
    }

    /// Default on-device location: Application Support, which survives
    /// launches and is backed up with the app data.
    static func defaultDirectory() throws -> URL {
        let base = try FileManager.default.url(
            for: .applicationSupportDirectory,
            in: .userDomainMask,
            appropriateFor: nil,
            create: true
        )
        return base.appendingPathComponent("SymDeskMobile/Outbox", isDirectory: true)
    }

    var all: [MobileOutboxEntry] { entries }

    func entry(id: UUID) -> MobileOutboxEntry? {
        entries.first { $0.id == id }
    }

    /// Pending (queued + uploading) entries, oldest first — the drain order.
    var pending: [MobileOutboxEntry] {
        entries
            .filter { $0.state == .queued || $0.state == .uploading }
            .sorted { $0.createdAt < $1.createdAt }
    }

    /// Failed entries, newest first, for the UI's failed-state surface.
    var failed: [MobileOutboxEntry] {
        entries
            .filter { $0.state == .failed }
            .sorted { $0.createdAt > $1.createdAt }
    }

    func enqueue(_ entry: MobileOutboxEntry) throws {
        entries.append(entry)
        try persist()
    }

    /// Replaces an entry (state transition, retry bookkeeping). A nil entry
    /// removes the stored one.
    func update(_ entry: MobileOutboxEntry?) throws {
        if let entry {
            if let index = entries.firstIndex(where: { $0.id == entry.id }) {
                entries[index] = entry
            } else {
                entries.append(entry)
            }
        }
        try persist()
    }

    func remove(id: UUID) throws {
        entries.removeAll { $0.id == id }
        try? FileManager.default.removeItem(at: payloadURL(for: id))
        try persist()
    }

    /// Removes every entry whose payload no longer exists — used when a
    /// vault is disconnected so stale writes cannot leak into a new vault.
    func removeAll() throws {
        entries.removeAll()
        try? FileManager.default.removeItem(at: payloadsURL)
        try FileManager.default.createDirectory(
            at: payloadsURL,
            withIntermediateDirectories: true
        )
        try persist()
    }

    // MARK: - Payload storage (uploads)

    func storePayload(_ data: Data, for id: UUID) throws {
        try data.write(to: payloadURL(for: id), options: .atomic)
    }

    func loadPayload(for id: UUID) throws -> Data {
        try Data(contentsOf: payloadURL(for: id))
    }

    private func payloadURL(for id: UUID) -> URL {
        payloadsURL.appendingPathComponent(id.uuidString)
    }

    // MARK: - Persistence

    private func persist() throws {
        let data = try encoder.encode(entries)
        try data.write(to: metadataURL, options: .atomic)
    }

    private static func loadMetadata(from url: URL, decoder: JSONDecoder) -> [MobileOutboxEntry] {
        guard let data = try? Data(contentsOf: url) else { return [] }
        return (try? decoder.decode([MobileOutboxEntry].self, from: data)) ?? []
    }
}
