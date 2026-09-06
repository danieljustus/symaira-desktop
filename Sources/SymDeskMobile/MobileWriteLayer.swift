import CryptoKit
import Foundation
import Network

// MARK: - Frontmatter + naming (contract-v6 compatible, mirrors desktop `note new`)

enum MobileNoteWriter {
    /// File name for a new note, matching `Service.NoteNew` on the desktop:
    /// spaces become underscores, extension `.md`. The returned name is
    /// safe to join into a vault-relative path (no separators, no dots).
    static func filename(for title: String) -> String {
        let trimmed = title.trimmingCharacters(in: .whitespacesAndNewlines)
        let base = trimmed.isEmpty ? "Note" : trimmed
        let cleaned = base
            .replacingOccurrences(of: " ", with: "_")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "\\", with: "_")
            .trimmingCharacters(in: CharacterSet(charactersIn: ".:"))
        return (cleaned.isEmpty ? "Note" : cleaned) + ".md"
    }

    /// Minimal contract-v6-compatible frontmatter plus body, byte-identical in shape to what
    /// `note new` writes (`title`, `created`, `tags: []`), so the desktop
    /// parser and `MobileVaultParser.parse` both accept the result.
    static func noteDocument(title: String, body: String, createdAt: Date = Date()) -> String {
        let iso = ISO8601DateFormatter().string(from: createdAt)
        return """
        ---
        title: "\(escaped(title))"
        created: "\(iso)"
        tags: []
        ---

        \(body)
        """
    }

    /// Sibling conflict file name understood by the desktop
    /// `conflict resolve` command (`deriveOriginalPath` strips the
    /// " conflicted copy" suffix). Vault-relative, no leading slash.
    static func conflictFilename(for path: String) -> String {
        let url = URL(fileURLWithPath: path)
        let base = url.deletingPathExtension().lastPathComponent
        let ext = url.pathExtension.isEmpty ? "" : "." + url.pathExtension
        let directory = url.deletingLastPathComponent().path
        let relative = directory == "/" ? "" : directory
        let joined = relative.isEmpty
            ? base + " conflicted copy" + ext
            : relative + "/" + base + " conflicted copy" + ext
        return joined.hasPrefix("/") ? String(joined.dropFirst()) : joined
    }

    private static func escaped(_ value: String) -> String {
        value.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"")
    }
}

// MARK: - Adapter protocol

/// A backend a queued write can be applied to. Implementations own their
/// precondition check (ETag / content hash vs mtime+size) and raise
/// `MobileWriteError` for the coordinator to map onto queue states.
protocol MobileWriteAdapter: Sendable {
    /// Applies one entry. Must not throw `conflict` without having
    /// preserved the losing version as a sibling conflict file first.
    func apply(_ entry: MobileOutboxEntry) async throws
}

// MARK: - Server adapter (PUT /api/v1/files, POST /api/v1/ingest)

/// Server-mode adapter. Precondition is content-based: the phone computes
/// the SHA-256 of the note content it last saw from the snapshot and
/// re-checks it against the live file before every write. The server has
/// no per-file ETag endpoint, so the hash comparison happens client-side
/// immediately before the PUT — best effort, exactly as documented for the
/// Files mode.
final class MobileServerWriteAdapter: MobileWriteAdapter {
    private let connection: MobileServerConnection
    private let session: URLSession

    init(connection: MobileServerConnection, session: URLSession = .shared) {
        self.connection = connection
        self.session = session
    }

    func apply(_ entry: MobileOutboxEntry) async throws {
        switch entry.kind {
        case .createNote, .updateNote:
            try await applyNote(entry)
        case .uploadOriginal:
            try await applyUpload(entry)
        }
    }

    private func applyNote(_ entry: MobileOutboxEntry) async throws {
        guard let content = entry.content else {
            throw MobileWriteError.invalidContent(reason: "note entry has no content")
        }

        // Precondition: compare the live file against what the phone last saw.
        let live = try await fetchLive(path: entry.path)
        let expected = entry.precondition.etag
        let hashMatches = live.map { Self.sha256($0) } == expected

        if entry.kind == .createNote {
            // Create must not clobber an existing file.
            if live != nil {
                let preserved = try await preserveAsConflict(entry, remoteExists: true)
                throw MobileWriteError.conflict(
                    preservedAt: preserved,
                    reason: "A note already exists at “\(entry.path)” on the server"
                )
            }
        } else if !hashMatches {
            let preserved = try await preserveAsConflict(entry, remoteExists: live != nil)
            throw MobileWriteError.conflict(
                preservedAt: preserved,
                reason: "The note changed on the server while this edit was queued"
            )
        }

        do {
            try await put(path: entry.path, content: content)
        } catch let error as MobileWriteError {
            throw error
        } catch {
            throw MobileWriteError.network(underlying: error.localizedDescription)
        }
    }

    private func applyUpload(_ entry: MobileOutboxEntry) async throws {
        guard let data = entry.originalData else {
            throw MobileWriteError.invalidContent(reason: "upload entry has no data")
        }
        let filename = entry.originalFilename ?? entry.path
        do {
            try await ingest(data: data, filename: filename)
        } catch let error as MobileWriteError {
            throw error
        } catch {
            throw MobileWriteError.network(underlying: error.localizedDescription)
        }
    }

    /// Writes the losing version next to the target as a " conflicted copy"
    /// sibling via the same PUT path, so nothing is lost and the desktop
    /// `conflict resolve` command understands the file.
    private func preserveAsConflict(_ entry: MobileOutboxEntry, remoteExists: Bool) async throws -> String {
        guard let content = entry.content else {
            throw MobileWriteError.invalidContent(reason: "note entry has no content")
        }
        let conflictPath = MobileNoteWriter.conflictFilename(for: entry.path)
        do {
            try await put(path: conflictPath, content: content)
            return conflictPath
        } catch let error as MobileWriteError {
            throw error
        } catch {
            throw MobileWriteError.network(underlying: error.localizedDescription)
        }
    }

    private func fetchLive(path: String) async throws -> Data? {
        var components = URLComponents(
            url: connection.url.appendingPathComponent("/api/v1/files"),
            resolvingAgainstBaseURL: false
        )
        components?.queryItems = [URLQueryItem(name: "path", value: path)]
        guard let url = components?.url else { throw MobileWriteError.invalidContent(reason: "invalid server URL") }

        var request = URLRequest(url: url)
        request.setValue("Bearer \(connection.token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.timeoutInterval = 60

        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw MobileWriteError.network(underlying: "invalid response")
        }
        if http.statusCode == 404 { return nil }
        guard (200..<300).contains(http.statusCode) else {
            throw MobileWriteError.rejected(status: http.statusCode, reason: "could not read current file state")
        }
        return data
    }

    private func put(path: String, content: String) async throws {
        guard let url = connection.url.appendingPathComponent("/api/v1/files")
            .appending(queryItems: [URLQueryItem(name: "path", value: path)]) as URL? else {
            throw MobileWriteError.invalidContent(reason: "invalid server URL")
        }
        var request = URLRequest(url: url)
        request.httpMethod = "PUT"
        request.setValue("Bearer \(connection.token)", forHTTPHeaderField: "Authorization")
        request.setValue("text/markdown; charset=utf-8", forHTTPHeaderField: "Content-Type")
        request.httpBody = Data(content.utf8)
        request.timeoutInterval = 60

        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw MobileWriteError.network(underlying: "invalid response")
        }
        guard (200..<300).contains(http.statusCode) else {
            let reason = (try? JSONDecoder().decode(ErrorEnvelope.self, from: data).error)
                ?? "HTTP \(http.statusCode)"
            throw MobileWriteError.rejected(status: http.statusCode, reason: reason)
        }
    }

    private func ingest(data: Data, filename: String) async throws {
        let boundary = "SymDeskMobile-\(UUID().uuidString)"
        var body = Data()
        body.append("--\(boundary)\r\n".data(using: .utf8)!)
        body.append("Content-Disposition: form-data; name=\"file\"; filename=\"\(filename)\"\r\n".data(using: .utf8)!)
        body.append("Content-Type: application/octet-stream\r\n\r\n".data(using: .utf8)!)
        body.append(data)
        body.append("\r\n--\(boundary)--\r\n".data(using: .utf8)!)

        let url = connection.url.appendingPathComponent("/api/v1/ingest")
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("Bearer \(connection.token)", forHTTPHeaderField: "Authorization")
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        request.httpBody = body
        request.timeoutInterval = 120

        let (responseData, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw MobileWriteError.network(underlying: "invalid response")
        }
        guard (200..<300).contains(http.statusCode) else {
            let reason = (try? JSONDecoder().decode(ErrorEnvelope.self, from: responseData).error)
                ?? "HTTP \(http.statusCode)"
            throw MobileWriteError.rejected(status: http.statusCode, reason: reason)
        }
    }

    static func sha256(_ data: Data) -> String {
        SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
    }

    private struct ErrorEnvelope: Decodable { let error: String }
}

// MARK: - Files adapter (coordinated writes via the security-scoped bookmark)

/// Files/iCloud-mode adapter. Writes go through `NSFileCoordinator` so the
/// file provider sees them as coordinated changes, exactly like the
/// existing read path (`MobileVaultScanner.coordinatedData`).
final class MobileFilesWriteAdapter: MobileWriteAdapter {
    private let vaultRoot: URL

    init(vaultRoot: URL) {
        self.vaultRoot = vaultRoot.standardizedFileURL
    }

    func apply(_ entry: MobileOutboxEntry) async throws {
        switch entry.kind {
        case .createNote, .updateNote:
            try await applyNote(entry)
        case .uploadOriginal:
            try await applyUpload(entry)
        }
    }

    private func applyNote(_ entry: MobileOutboxEntry) async throws {
        guard let content = entry.content else {
            throw MobileWriteError.invalidContent(reason: "note entry has no content")
        }
        let target = vaultRoot.appendingPathComponent(entry.path)

        // Precondition: mtime+size of the file the phone last parsed.
        let live = fileSignature(at: target)
        if entry.kind == .createNote {
            if live != nil {
                let preserved = try preserveLocally(entry, conflictPath: MobileNoteWriter.conflictFilename(for: entry.path))
                throw MobileWriteError.conflict(
                    preservedAt: preserved,
                    reason: "A note already exists at “\(entry.path)”"
                )
            }
        } else {
            let expected = entry.precondition
            let matches = live.map { $0.modifiedAt == expected.modifiedAt && $0.size == expected.size } ?? false
            if !matches {
                let preserved = try preserveLocally(entry, conflictPath: MobileNoteWriter.conflictFilename(for: entry.path))
                throw MobileWriteError.conflict(
                    preservedAt: preserved,
                    reason: "The note changed on disk while this edit was queued"
                )
            }
        }

        try writeCoordinated(Data(content.utf8), to: target)
    }

    private func applyUpload(_ entry: MobileOutboxEntry) async throws {
        guard let data = entry.originalData else {
            throw MobileWriteError.invalidContent(reason: "upload entry has no data")
        }
        // Files-mode uploads land in the consume folder (`inbox_watch`),
        // which the desktop watcher picks up — no on-device ingest needed.
        let consumeFolder = entry.folder ?? "inbox_watch"
        let target = vaultRoot
            .appendingPathComponent(consumeFolder, isDirectory: true)
            .appendingPathComponent(entry.originalFilename ?? entry.path)
        try writeCoordinated(data, to: target)
    }

    /// Preserves the losing (local) version as a sibling conflict file and
    /// returns its vault-relative path for the UI.
    private func preserveLocally(_ entry: MobileOutboxEntry, conflictPath: String) throws -> String {
        guard let content = entry.content else {
            throw MobileWriteError.invalidContent(reason: "note entry has no content")
        }
        let target = vaultRoot.appendingPathComponent(conflictPath)
        try writeCoordinated(Data(content.utf8), to: target)
        return conflictPath
    }

    private func writeCoordinated(_ data: Data, to target: URL) throws {
        let parent = target.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: parent, withIntermediateDirectories: true)

        let coordinator = NSFileCoordinator(filePresenter: nil)
        var coordinationError: NSError?
        var writeError: Error?
        coordinator.coordinate(
            writingItemAt: target,
            options: [.forReplacing],
            error: &coordinationError
        ) { coordinatedURL in
            do {
                try data.write(to: coordinatedURL, options: .atomic)
            } catch {
                writeError = error
            }
        }
        if let coordinationError { throw coordinationError }
        if let writeError { throw writeError }
    }

    private func fileSignature(at url: URL) -> (modifiedAt: Date, size: Int)? {
        guard let values = try? url.resourceValues(forKeys: [.isRegularFileKey, .contentModificationDateKey, .fileSizeKey]),
              values.isRegularFile == true else { return nil }
        return (values.contentModificationDate ?? .distantPast, values.fileSize ?? 0)
    }
}

// MARK: - Coordinator (adapter selection, drain, backoff, connectivity)

/// Owns the outbox, applies entries through the adapter for the active
/// connection mode and drains the queue with exponential backoff. Draining
/// is triggered on app launch, on connectivity return, on mode switches and
/// after every successful reload. `onChange` fires whenever the visible
/// queue state may have changed, so the UI can refresh its banner.
actor MobileWriteCoordinator {
    private let outbox: MobileOutbox
    private var adapter: (any MobileWriteAdapter)?
    private let monitor: MobileConnectivityMonitor
    private var isDraining = false
    /// Called (off-main) after any mutation of the queue. The store hops to
    /// the main actor to republish its `outboxEntries`.
    var onChange: (@Sendable () -> Void)?

    private let backoffBase: TimeInterval = 2
    private let backoffCap: TimeInterval = 15 * 60

    init(
        outbox: MobileOutbox,
        monitor: MobileConnectivityMonitor = MobileConnectivityMonitor()
    ) {
        self.outbox = outbox
        self.monitor = monitor
        monitor.onSatisfied = { [weak self] in
            Task { await self?.drain() }
        }
        monitor.start()
    }

    /// Switches the active backend. Called by the store whenever the
    /// connection mode changes (files → server, server → files, reset).
    func setMode(_ adapter: (any MobileWriteAdapter)?) {
        self.adapter = adapter
        Task { await drain() }
    }

    /// Installs the change callback (see `onChange`).
    func setOnChange(_ handler: (@Sendable () -> Void)?) {
        onChange = handler
    }

    func entries() async -> [MobileOutboxEntry] { await outbox.all }
    func pendingCount() async -> Int { await outbox.pending.count }
    func failedEntries() async -> [MobileOutboxEntry] { await outbox.failed }

    func enqueue(_ entry: MobileOutboxEntry) async throws {
        try await outbox.enqueue(entry)
        if let data = entry.originalData {
            try await outbox.storePayload(data, for: entry.id)
        }
        onChange?()
        Task { await drain() }
    }

    func retry(id: UUID) async throws {
        guard var entry = await outbox.entry(id: id), entry.state == .failed else { return }
        entry.state = .queued
        entry.nextRetryAt = nil
        entry.lastError = nil
        entry.conflictPath = nil
        try await outbox.update(entry)
        onChange?()
        Task { await drain() }
    }

    func remove(id: UUID) async throws {
        try await outbox.remove(id: id)
        onChange?()
    }

    func clear() async throws {
        try await outbox.removeAll()
        onChange?()
    }

    /// Applies every due queued entry. Entries with a future `nextRetryAt`
    /// are skipped; failures are classified: network → keep queued with
    /// backoff, everything else → visible failed state with the reason.
    func drain() async {
        guard !isDraining else { return }
        isDraining = true
        defer { isDraining = false }

        while let entry = await nextDue() {
            guard let adapter else { return }

            var working = entry
            working.state = .uploading
            try? await outbox.update(working)

            do {
                try await adapter.apply(working)
                try? await outbox.remove(id: working.id)
            } catch let error as MobileWriteError {
                switch error {
                case .network:
                    working.state = .queued
                    working.attempts += 1
                    working.lastError = error.localizedDescription
                    working.nextRetryAt = Date().addingTimeInterval(backoff(after: working.attempts))
                    try? await outbox.update(working)
                case .rejected, .conflict, .invalidContent:
                    working.state = .failed
                    working.lastError = error.localizedDescription
                    if case .conflict(let preservedAt, _) = error {
                        working.conflictPath = preservedAt
                    }
                    try? await outbox.update(working)
                }
            } catch {
                working.state = .queued
                working.attempts += 1
                working.lastError = error.localizedDescription
                working.nextRetryAt = Date().addingTimeInterval(backoff(after: working.attempts))
                try? await outbox.update(working)
            }
        }
        onChange?()
    }

    private func nextDue() async -> MobileOutboxEntry? {
        let now = Date()
        let pending = await outbox.pending
        return pending.first { entry in
            entry.nextRetryAt.map { $0 <= now } ?? true
        }
    }

    private func backoff(after attempts: Int) -> TimeInterval {
        let delay = backoffBase * pow(2, Double(max(0, attempts - 1)))
        return min(delay, backoffCap)
    }
}

/// Thin NWPathMonitor wrapper so the coordinator can observe connectivity
/// without owning the runloop. `onSatisfied` fires when the path becomes
/// satisfied (offline → online transition).
final class MobileConnectivityMonitor: @unchecked Sendable {
    var onSatisfied: (() -> Void)?
    private let monitor = NWPathMonitor()
    private let queue = DispatchQueue(label: "symdesk.mobile.connectivity")
    private var wasSatisfied = false

    func start() {
        monitor.pathUpdateHandler = { [weak self] path in
            guard let self else { return }
            let satisfied = path.status == .satisfied
            if satisfied, !self.wasSatisfied {
                self.onSatisfied?()
            }
            self.wasSatisfied = satisfied
        }
        monitor.start(queue: queue)
    }
}
