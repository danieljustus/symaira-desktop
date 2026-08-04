import Combine
import Foundation
import WidgetKit

struct MobileNote: Identifiable, Hashable, Sendable {
    let path: String
    let title: String
    let body: String
    let rawContent: String
    let tags: [String]
    let created: String
    let modifiedAt: Date
    /// Byte size of the source file at parse time — part of the
    /// mtime+size signature used for cache and search-index invalidation.
    let fileSize: Int
    let documentDate: String
    let person: String
    let status: String
    let dueDate: String
    let confidence: Int
    let correspondent: String
    let documentType: String
    let asn: Int
    let attachmentReferences: [String]
    let searchText: String

    var id: String { path }

    var isDocument: Bool {
        !documentType.isEmpty
            || !status.isEmpty
            || !dueDate.isEmpty
            || confidence > 0
            || !attachmentReferences.isEmpty
    }

    var filename: String {
        URL(fileURLWithPath: path).lastPathComponent
    }

    func fileURL(in root: URL) -> URL {
        root.appendingPathComponent(path).standardizedFileURL
    }

    func attachmentURL(in root: URL) -> URL? {
        let root = root.standardizedFileURL.resolvingSymlinksInPath()
        let noteDirectory = fileURL(in: root).deletingLastPathComponent()

        for reference in attachmentReferences {
            let cleaned = MobileVaultParser.cleanedReference(reference)
            guard !cleaned.isEmpty else { continue }

            let candidates: [URL]
            if let fileURL = URL(string: cleaned), fileURL.isFileURL {
                candidates = [fileURL]
            } else if cleaned.contains("://") {
                continue
            } else if cleaned.hasPrefix("/") {
                candidates = [URL(fileURLWithPath: cleaned)]
            } else {
                candidates = [
                    noteDirectory.appendingPathComponent(cleaned),
                    root.appendingPathComponent(cleaned)
                ]
            }

            for candidate in candidates {
                let resolved = candidate.standardizedFileURL.resolvingSymlinksInPath()
                guard resolved.path == root.path || resolved.path.hasPrefix(root.path + "/") else { continue }
                if FileManager.default.fileExists(atPath: resolved.path) {
                    return resolved
                }
            }
        }
        return nil
    }
}

struct MobileVaultSnapshot: Sendable {
    let notes: [MobileNote]
    let skippedFiles: Int
}

enum MobileVaultError: LocalizedError {
    case unreadableVault
    case invalidBookmark

    var errorDescription: String? {
        switch self {
        case .unreadableVault:
            return "SymDesk could not read this folder. Select the vault again in Files."
        case .invalidBookmark:
            return "The saved vault permission expired. Select the vault again."
        }
    }
}

enum MobileVaultParser {
    private static let previewableExtensions: Set<String> = [
        "pdf", "png", "jpg", "jpeg", "heic", "gif", "tiff", "webp",
        "txt", "rtf", "csv", "json", "eml",
        "doc", "docx", "xls", "xlsx", "ppt", "pptx",
        "odt", "ods", "odp", "pages", "numbers", "key"
    ]

    static func parse(data: Data, fileURL: URL, root: URL, modifiedAt: Date) throws -> MobileNote {
        guard let source = String(data: data, encoding: .utf8) else {
            throw CocoaError(.fileReadInapplicableStringEncoding)
        }

        let parsed = splitFrontmatter(source)
        let values = parsed.values
        let relativePath = relativePath(for: fileURL, root: root)
        let fallbackTitle = fileURL.deletingPathExtension().lastPathComponent
        let title = scalar(values["title"]) ?? fallbackTitle
        let tags = parsed.lists["tags"] ?? inlineList(values["tags"])

        var references = ["archive_path", "source_path", "original_path"]
            .compactMap { scalar(values[$0]) }
        references.append(contentsOf: attachmentReferences(in: parsed.body))
        references = orderedUnique(references.filter(isPreviewableReference))

        let documentDate = scalar(values["document_date"]) ?? ""
        let person = scalar(values["person"]) ?? ""
        let status = scalar(values["status"]) ?? ""
        let dueDate = scalar(values["due_date"]) ?? ""
        let confidence = Int(scalar(values["confidence"]) ?? "") ?? 0
        let correspondent = scalar(values["correspondent"]) ?? ""
        let documentType = scalar(values["document_type"]) ?? ""
        let asn = Int(scalar(values["asn"]) ?? "") ?? 0
        let created = scalar(values["created"]) ?? scalar(values["date"]) ?? ""

        let searchable = [
            title,
            relativePath,
            tags.joined(separator: " "),
            correspondent,
            documentType,
            person,
            status,
            parsed.body
        ]
        .joined(separator: "\n")
        .folding(options: [.caseInsensitive, .diacriticInsensitive], locale: .current)
        .lowercased()

        return MobileNote(
            path: relativePath,
            title: title,
            body: parsed.body,
            rawContent: source,
            tags: tags,
            created: created,
            modifiedAt: modifiedAt,
            fileSize: data.count,
            documentDate: documentDate,
            person: person,
            status: status,
            dueDate: dueDate,
            confidence: confidence,
            correspondent: correspondent,
            documentType: documentType,
            asn: asn,
            attachmentReferences: references,
            searchText: searchable
        )
    }

    static func normalizedSearchQuery(_ query: String) -> String {
        query
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .folding(options: [.caseInsensitive, .diacriticInsensitive], locale: .current)
            .lowercased()
    }

    static func cleanedReference(_ reference: String) -> String {
        var value = scalar(reference) ?? ""
        if let pipe = value.firstIndex(of: "|") {
            value = String(value[..<pipe])
        }
        if let hash = value.firstIndex(of: "#") {
            value = String(value[..<hash])
        }
        if let query = value.firstIndex(of: "?") {
            value = String(value[..<query])
        }
        return value.removingPercentEncoding ?? value
    }

    private static func splitFrontmatter(_ source: String) -> (
        values: [String: String],
        lists: [String: [String]],
        body: String
    ) {
        let normalized = source.replacingOccurrences(of: "\r\n", with: "\n")
        let lines = normalized.components(separatedBy: "\n")
        guard lines.first?.trimmingCharacters(in: .whitespaces) == "---",
              let end = lines.dropFirst().firstIndex(where: {
                  $0.trimmingCharacters(in: .whitespaces) == "---"
              }) else {
            return ([:], [:], normalized)
        }

        var values: [String: String] = [:]
        var lists: [String: [String]] = [:]
        var activeListKey: String?

        for line in lines[1..<end] {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty, !trimmed.hasPrefix("#") else { continue }

            if trimmed.hasPrefix("- "), let activeListKey {
                let value = String(trimmed.dropFirst(2))
                if let scalar = scalar(value), !scalar.isEmpty {
                    lists[activeListKey, default: []].append(scalar)
                }
                continue
            }

            guard let colon = line.firstIndex(of: ":") else {
                activeListKey = nil
                continue
            }
            let key = line[..<colon].trimmingCharacters(in: .whitespaces)
            guard !key.isEmpty, !key.contains(" ") else {
                activeListKey = nil
                continue
            }
            let value = line[line.index(after: colon)...].trimmingCharacters(in: .whitespaces)
            values[key] = value
            activeListKey = value.isEmpty ? key : nil
        }

        let bodyStart = lines.index(after: end)
        let body = bodyStart < lines.endIndex
            ? lines[bodyStart...].joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
            : ""
        return (values, lists, body)
    }

    private static func attachmentReferences(in markdown: String) -> [String] {
        let patterns = [
            #"!\[\[[^\]]+\]\]"#,
            #"!?\[[^\]]*\]\([^\)]+\)"#
        ]
        var references: [String] = []
        let fullRange = NSRange(markdown.startIndex..<markdown.endIndex, in: markdown)

        for pattern in patterns {
            guard let regex = try? NSRegularExpression(pattern: pattern) else { continue }
            for match in regex.matches(in: markdown, range: fullRange) {
                guard let range = Range(match.range, in: markdown) else { continue }
                let token = String(markdown[range])
                if token.hasPrefix("![[") {
                    references.append(String(token.dropFirst(3).dropLast(2)))
                } else if let open = token.lastIndex(of: "("), token.hasSuffix(")") {
                    references.append(String(token[token.index(after: open)..<token.index(before: token.endIndex)]))
                }
            }
        }
        return references
    }

    private static func isPreviewableReference(_ reference: String) -> Bool {
        let pathExtension = URL(fileURLWithPath: cleanedReference(reference)).pathExtension.lowercased()
        return previewableExtensions.contains(pathExtension)
    }

    private static func relativePath(for fileURL: URL, root: URL) -> String {
        let rootComponents = root.standardizedFileURL.pathComponents
        let fileComponents = fileURL.standardizedFileURL.pathComponents
        guard fileComponents.starts(with: rootComponents) else { return fileURL.lastPathComponent }
        return fileComponents.dropFirst(rootComponents.count).joined(separator: "/")
    }

    private static func inlineList(_ raw: String?) -> [String] {
        guard var raw else { return [] }
        raw = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard raw.hasPrefix("["), raw.hasSuffix("]") else { return [] }
        return raw.dropFirst().dropLast().split(separator: ",").compactMap { scalar(String($0)) }
    }

    private static func scalar(_ raw: String?) -> String? {
        guard var value = raw?.trimmingCharacters(in: .whitespacesAndNewlines), !value.isEmpty else {
            return nil
        }
        if value.count >= 2,
           (value.hasPrefix("\"") && value.hasSuffix("\"") || value.hasPrefix("'") && value.hasSuffix("'")) {
            value.removeFirst()
            value.removeLast()
        }
        return value.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private static func orderedUnique(_ values: [String]) -> [String] {
        var seen: Set<String> = []
        return values.filter { seen.insert($0).inserted }
    }
}

actor MobileVaultScanner {
    private struct Signature: Equatable, Sendable {
        let modifiedAt: Date
        let size: Int
    }

    private struct CacheEntry: Sendable {
        let signature: Signature
        let note: MobileNote
    }

    private var cache: [String: CacheEntry] = [:]
    private let maximumMarkdownSize = 8 * 1_024 * 1_024

    func scan(root: URL) throws -> MobileVaultSnapshot {
        let keys: [URLResourceKey] = [.isRegularFileKey, .contentModificationDateKey, .fileSizeKey]
        guard let enumerator = FileManager.default.enumerator(
            at: root,
            includingPropertiesForKeys: keys,
            options: [.skipsHiddenFiles, .skipsPackageDescendants]
        ) else {
            throw MobileVaultError.unreadableVault
        }

        var notes: [MobileNote] = []
        var seen: Set<String> = []
        var skippedFiles = 0

        for case let fileURL as URL in enumerator {
            guard fileURL.pathExtension.lowercased() == "md" else { continue }
            do {
                let values = try fileURL.resourceValues(forKeys: Set(keys))
                guard values.isRegularFile == true else { continue }

                let size = values.fileSize ?? 0
                guard size <= maximumMarkdownSize else {
                    skippedFiles += 1
                    continue
                }

                let modifiedAt = values.contentModificationDate ?? .distantPast
                let signature = Signature(modifiedAt: modifiedAt, size: size)
                let cacheKey = fileURL.standardizedFileURL.path
                seen.insert(cacheKey)

                if let cached = cache[cacheKey], cached.signature == signature {
                    notes.append(cached.note)
                    continue
                }

                let data = try coordinatedData(at: fileURL)
                let note = try MobileVaultParser.parse(
                    data: data,
                    fileURL: fileURL,
                    root: root,
                    modifiedAt: modifiedAt
                )
                cache[cacheKey] = CacheEntry(signature: signature, note: note)
                notes.append(note)
            } catch {
                skippedFiles += 1
            }
        }

        cache = cache.filter { seen.contains($0.key) }
        notes.sort {
            if $0.modifiedAt != $1.modifiedAt { return $0.modifiedAt > $1.modifiedAt }
            return $0.title.localizedStandardCompare($1.title) == .orderedAscending
        }
        return MobileVaultSnapshot(notes: notes, skippedFiles: skippedFiles)
    }

    func clearCache() {
        cache.removeAll(keepingCapacity: false)
    }

    private func coordinatedData(at url: URL) throws -> Data {
        let coordinator = NSFileCoordinator(filePresenter: nil)
        var coordinationError: NSError?
        var result: Result<Data, Error>?
        coordinator.coordinate(readingItemAt: url, options: [], error: &coordinationError) { coordinatedURL in
            result = Result { try Data(contentsOf: coordinatedURL, options: [.mappedIfSafe]) }
        }
        if let coordinationError { throw coordinationError }
        guard let result else { throw MobileVaultError.unreadableVault }
        return try result.get()
    }
}

@MainActor
final class MobileVaultStore: ObservableObject {
    @Published private(set) var vaultURL: URL?
    @Published private(set) var notes: [MobileNote] = []
    @Published private(set) var isLoading = false
    @Published private(set) var skippedFiles = 0
    @Published private(set) var revision = 0
	@Published private(set) var serverURL: URL?
    @Published var errorMessage: String?
    @Published private(set) var recentlyOpenedPaths: [String] = []
    /// Visible write-queue state (pending + failed entries) for the UI.
    @Published private(set) var outboxEntries: [MobileOutboxEntry] = []
    /// Vault-relative path requested via a deep link (Spotlight tap or
    /// `symdesk://open/<path>`). The root view presents the note when set.
    @Published var pendingOpenPath: String?

    private let scanner = MobileVaultScanner()
    private let bookmarkKey = "symdesk.mobile.vault-bookmark.v1"
    private var hasSecurityScope = false
    private var loadGeneration = 0
	private var remoteClient: MobileRemoteClient?
	private var snapshotETag: String?
    private let outbox: MobileOutbox
    private let writeCoordinator: MobileWriteCoordinator
    private let shareInbox: MobileShareInbox?
    private let cacheURL: URL
    /// Feeds vault content into iOS Core Spotlight (home-screen search).
    private let spotlightIndexer = MobileSpotlightIndexer()
    /// Ranked, persisted on-device search index. Fed from the parsed
    /// snapshot after every reload in both connection modes.
    private let searchIndex: MobileSearchIndex

    init(outbox: MobileOutbox? = nil, cacheURL: URL = MobileVaultCache.defaultURL(), searchIndex: MobileSearchIndex? = nil) {
        let resolvedOutbox: MobileOutbox
        if let outbox {
            resolvedOutbox = outbox
        } else {
            // The default location lives in Application Support; tests inject
            // a temporary directory instead.
            resolvedOutbox = (try? MobileOutbox(directory: MobileOutbox.defaultDirectory()))
                ?? (try? MobileOutbox(directory: FileManager.default.temporaryDirectory
                    .appendingPathComponent("SymDeskMobile-Outbox-\(UUID().uuidString)")))!
        }
        self.outbox = resolvedOutbox
        self.writeCoordinator = MobileWriteCoordinator(outbox: resolvedOutbox)
        self.shareInbox = MobileShareInbox(coordinator: self.writeCoordinator)
        self.cacheURL = cacheURL
        if let searchIndex {
            self.searchIndex = searchIndex
        } else {
            let base = (try? FileManager.default.url(
                for: .applicationSupportDirectory,
                in: .userDomainMask,
                appropriateFor: nil,
                create: true
            )) ?? FileManager.default.temporaryDirectory
            self.searchIndex = MobileSearchIndex(
                fileURL: base.appendingPathComponent("SymDeskMobile/search-index.json")
            )
        }
        // Cache-first launch: show the last parsed snapshot immediately,
        // then refresh in the background. The refresh replaces `notes`.
        if let cache = MobileVaultCache.load(from: cacheURL) {
            notes = cache.notes.map(Self.note(from:))
            skippedFiles = cache.skippedFiles
        }
        Task { await writeCoordinator.setOnChange { [weak self] in
            Task { @MainActor in await self?.refreshOutboxState() }
        } }
		if let connection = MobileServerConfig.connection() {
			serverURL = connection.url
			remoteClient = MobileRemoteClient(connection: connection)
			Task { await writeCoordinator.setMode(MobileServerWriteAdapter(connection: connection)) }
		} else {
			restoreVault()
			if let root = vaultURL {
				Task { await writeCoordinator.setMode(MobileFilesWriteAdapter(vaultRoot: root)) }
			}
		}
		recentlyOpenedPaths = MobileRecentsStore.read().map(\.path)
        Task { await refreshOutboxState() }
        Task { await writeCoordinator.drain() }
        drainShareInbox()
    }

    /// Moves share-extension items into the write layer. Called on launch
    /// and whenever a backend becomes active, so shares filed while the
    /// app was closed are queued as soon as the app can write.
    private func drainShareInbox() {
        Task {
            await shareInbox?.drain()
            await refreshOutboxState()
        }
    }

    /// Republish the outbox's visible state on the main actor.
    private func refreshOutboxState() async {
        outboxEntries = await writeCoordinator.entries()
    }

    /// Pending (queued + uploading) writes, for the workspace banner.
    var pendingWriteCount: Int {
        outboxEntries.filter { $0.state == .queued || $0.state == .uploading }.count
    }

    /// Failed writes with reasons, for the settings surface.
    var failedWrites: [MobileOutboxEntry] {
        outboxEntries.filter { $0.state == .failed }
    }

    // MARK: - Write API (all capture features route through the outbox)

    /// Creates a new note through the outbox: contract-v2 frontmatter and
    /// desktop-conformant file naming are applied here, so every consumer
    /// (composer, share extension, intents) writes the same shape.
    /// `folder` is an optional vault-relative target folder (default: vault
    /// root, matching desktop `note new`). Returns the vault-relative path.
    func enqueueCreateNote(title: String, body: String, folder: String? = nil) async throws -> String {
        let filename = MobileNoteWriter.filename(for: title)
        let path = folder.map { ($0 as NSString).appendingPathComponent(filename) } ?? filename
        let content = MobileNoteWriter.noteDocument(title: title, body: body)
        let entry = MobileOutboxEntry(kind: .createNote, path: path, content: content, folder: folder)
        try await writeCoordinator.enqueue(entry)
        return path
    }

    /// Queues an update of an existing note. The precondition captures what
    /// the phone last saw (content hash in server mode, mtime+size in Files
    /// mode) so a remote change made meanwhile produces a conflict file
    /// instead of being overwritten.
    func enqueueUpdateNote(_ note: MobileNote, content: String) async throws {
        let precondition: MobileWritePrecondition
        if let remoteClient {
            precondition = MobileWritePrecondition(
                etag: MobileServerWriteAdapter.sha256(Data(note.rawContent.utf8))
            )
        } else if let root = vaultURL {
            let url = note.fileURL(in: root)
            let values = try? url.resourceValues(forKeys: [.contentModificationDateKey, .fileSizeKey])
            precondition = MobileWritePrecondition(
                modifiedAt: values?.contentModificationDate,
                size: values?.fileSize
            )
        } else {
            precondition = .none
        }
        let entry = MobileOutboxEntry(
            kind: .updateNote,
            path: note.path,
            content: content,
            precondition: precondition
        )
        try await writeCoordinator.enqueue(entry)
    }

    /// Queues an original (scan, PDF, image) for ingestion. In server mode
    /// it is uploaded to `POST /api/v1/ingest`; in Files mode it is written
    /// into the consume folder (`consumeFolder` defaults to `inbox_watch`).
    func enqueueUpload(data: Data, filename: String, consumeFolder: String? = nil) async throws {
        let entry = MobileOutboxEntry(
            kind: .uploadOriginal,
            path: filename,
            originalData: data,
            originalFilename: filename,
            folder: consumeFolder
        )
        try await writeCoordinator.enqueue(entry)
    }

    func retryWrite(id: UUID) async {
        try? await writeCoordinator.retry(id: id)
    }

    func removeWrite(id: UUID) async {
        try? await writeCoordinator.remove(id: id)
    }

    /// Converts a cached note back into the full in-memory shape. The body
    /// is the stored preview — a full reload replaces it shortly after.
    private static func note(from cached: MobileVaultCache.CachedNote) -> MobileNote {
        MobileNote(
            path: cached.path,
            title: cached.title,
            body: cached.bodyPreview,
            rawContent: cached.bodyPreview,
            tags: cached.tags,
            created: "",
            modifiedAt: cached.modifiedAt,
            fileSize: 0,
            documentDate: "",
            person: "",
            status: cached.status,
            dueDate: cached.dueDate,
            confidence: 0,
            correspondent: "",
            documentType: cached.documentType,
            asn: 0,
            attachmentReferences: [],
            searchText: [cached.title, cached.tags.joined(separator: " "), cached.bodyPreview]
                .joined(separator: "\n")
                .folding(options: [.caseInsensitive, .diacriticInsensitive], locale: .current)
                .lowercased()
        )
    }

    /// Ranked search over the persisted on-device index.
    func search(_ query: String, limit: Int = 50) async -> [MobileSearchIndex.Result] {
        await searchIndex.search(query: query, limit: limit)
    }

	/// Notes for `recentlyOpenedPaths`, most-recently-opened first, limited to
	/// notes that still exist in the current snapshot.
	var recentlyOpened: [MobileNote] {
		let byPath = Dictionary(uniqueKeysWithValues: notes.map { ($0.path, $0) })
		return recentlyOpenedPaths.compactMap { byPath[$0] }
	}

	/// Records `note` as opened, moving it to the front of the recents list
	/// and persisting across launches. Call whenever a note is opened.
	func recordOpened(_ note: MobileNote) {
		MobileRecentsStore.record(path: note.path, title: note.title)
		recentlyOpenedPaths = MobileRecentsStore.read().map(\.path)
		// Keep the home-screen widget's recents in sync.
		WidgetCenter.shared.reloadAllTimelines()
	}

	/// Resolves a deep link (`symdesk://open/<path>`) from Spotlight, a
	/// Handoff activity or a manual URL open. Presents the note when it is
	/// already in the snapshot; otherwise triggers a reload and retries
	/// once the snapshot arrives.
	func openDeepLink(_ url: URL) {
		guard let path = MobileSpotlightIndexer.path(from: url), !path.isEmpty else { return }
		if notes.contains(where: { $0.path == path }) {
			pendingOpenPath = path
			return
		}
		// The note may not be in the (possibly cached) snapshot yet.
		Task {
			await reload()
			if notes.contains(where: { $0.path == path }) {
				pendingOpenPath = path
			} else {
				errorMessage = "The note “\(path)” is not in this vault."
			}
		}
	}

	/// Resolves a deep link (`symdesk://open/<path>`) from Spotlight, a
	/// Handoff activity or a manual URL open. Presents the note when it is
	/// already in the snapshot; otherwise triggers a reload and retries
	/// once the snapshot arrives.
	func openDeepLink(_ url: URL) {
		guard let path = MobileSpotlightIndexer.path(from: url), !path.isEmpty else { return }
		if notes.contains(where: { $0.path == path }) {
			pendingOpenPath = path
			return
		}
		// The note may not be in the (possibly cached) snapshot yet.
		Task {
			await reload()
			if notes.contains(where: { $0.path == path }) {
				pendingOpenPath = path
			} else {
				errorMessage = "The note “\(path)” is not in this vault."
			}
		}
	}

	var isConfigured: Bool { vaultURL != nil || remoteClient != nil }
	var isRemote: Bool { remoteClient != nil }
	var displayLocation: String { serverURL?.absoluteString ?? vaultURL?.path ?? "No vault" }

    var documents: [MobileNote] {
        notes.filter(\.isDocument)
    }

    func selectVault(_ url: URL) {
		MobileServerConfig.reset()
		remoteClient = nil
		snapshotETag = nil
		serverURL = nil
        activate(url)
        Task { await writeCoordinator.setMode(MobileFilesWriteAdapter(vaultRoot: url.standardizedFileURL)) }
        drainShareInbox()
        do {
            let data = try url.bookmarkData(
                options: [.minimalBookmark],
                includingResourceValuesForKeys: nil,
                relativeTo: nil
            )
            UserDefaults.standard.set(data, forKey: bookmarkKey)
        } catch {
            errorMessage = "The folder opened, but its permission could not be saved: \(error.localizedDescription)"
        }
        Task { await reload() }
    }

    func reload() async {
		guard vaultURL != nil || remoteClient != nil else { return }
        loadGeneration += 1
        let generation = loadGeneration
        isLoading = true
        errorMessage = nil

        do {
			if let remoteClient {
				switch try await remoteClient.snapshot(ifNoneMatch: snapshotETag) {
				case .unchanged:
					guard generation == loadGeneration else { return }
					// Nothing to re-parse or re-render: the server confirmed
					// the vault matches what we already have.
				case .updated(let snapshot, let etag):
					guard generation == loadGeneration else { return }
					notes = snapshot.notes
					skippedFiles = snapshot.skippedFiles
					revision += 1
					snapshotETag = etag
					// Both connection modes feed the same index path; the
					// merge is incremental by mtime+size signature.
					await searchIndex.merge(snapshot: snapshot.notes)
				}
			} else if let root = vaultURL {
				let snapshot = try await scanner.scan(root: root)
				guard generation == loadGeneration else { return }
				notes = snapshot.notes
				skippedFiles = snapshot.skippedFiles
				revision += 1
				await searchIndex.merge(snapshot: snapshot.notes)
			} else {
				return
			}
        } catch {
            guard generation == loadGeneration else { return }
            errorMessage = error.localizedDescription
        }

        if generation == loadGeneration {
            isLoading = false
        }
        // A successful reload means the backend is reachable again: give the
        // outbox a chance to apply anything queued while offline.
        await writeCoordinator.drain()

        // Persist the fresh snapshot for cache-first launch and feed the
        // system search index. Only a successful reload replaces the cache
        // and the index; a failed refresh must not wipe the last good state.
        if errorMessage == nil {
            persistCache(snapshot: notes, skipped: skippedFiles)
            spotlightIndexer.replace(with: notes)
        }
    }

    /// Writes the compact snapshot cache used for cache-first launch.
    private func persistCache(snapshot: [MobileNote], skipped: Int) {
        let cached = snapshot.map { note in
            MobileVaultCache.CachedNote(
                path: note.path,
                title: note.title,
                bodyPreview: String(note.body.prefix(600)),
                tags: note.tags,
                documentType: note.documentType,
                status: note.status,
                dueDate: note.dueDate,
                modifiedAt: note.modifiedAt
            )
        }
        MobileVaultCache(notes: cached, skippedFiles: skipped, savedAt: Date()).save(to: cacheURL)
    }

    /// Test hook: applies a snapshot exactly like a successful reload would,
    /// including the cache write, without touching the network or scanner.
    func replaceForTesting(notes: [MobileNote], skipped: Int) {
        self.notes = notes
        skippedFiles = skipped
        revision += 1
        persistCache(snapshot: notes, skipped: skipped)
    }

	func connectServer(url: String, token: String) async throws {
		let connection = try MobileServerConfig.save(url: url, token: token)
		let client = MobileRemoteClient(connection: connection)
		do {
			try await client.status()
		} catch {
			MobileServerConfig.reset()
			throw error
		}
		if hasSecurityScope { vaultURL?.stopAccessingSecurityScopedResource() }
		hasSecurityScope = false
		vaultURL = nil
		UserDefaults.standard.removeObject(forKey: bookmarkKey)
		remoteClient = client
		snapshotETag = nil
		serverURL = connection.url
		notes = []
		await writeCoordinator.setMode(MobileServerWriteAdapter(connection: connection))
		await reload()
        drainShareInbox()
	}

	func attachmentURL(for note: MobileNote) async -> URL? {
		if let root = vaultURL { return note.attachmentURL(in: root) }
		guard let remoteClient else { return nil }
		return try? await remoteClient.cachedAttachment(for: note)
	}

    func resetVault() {
        loadGeneration += 1
        if hasSecurityScope {
            vaultURL?.stopAccessingSecurityScopedResource()
        }
        hasSecurityScope = false
        vaultURL = nil
		serverURL = nil
		remoteClient = nil
		snapshotETag = nil
        notes = []
        skippedFiles = 0
        revision += 1
        UserDefaults.standard.removeObject(forKey: bookmarkKey)
		MobileServerConfig.reset()
		recentlyOpenedPaths = []
		MobileRecentsStore.clear()
        // A disconnected vault must not leak queued writes into a new vault:
        // the whole queue is dropped and the write backend is detached.
        Task { await writeCoordinator.setMode(nil) }
        Task { try? await writeCoordinator.clear() }
        Task { await scanner.clearCache() }
        // Disconnecting revokes access: purge the local snapshot cache and
        // the system search index so no vault content stays on the device
        // or in Spotlight after the user removes the vault.
        MobileVaultCache.remove(at: cacheURL)
        spotlightIndexer.removeAll()
        // The index belongs to the disconnected vault: purge it so stale
        // results cannot surface after the user revokes access.
        Task { await searchIndex.removeAll() }
    }

    private func restoreVault() {
        guard let data = UserDefaults.standard.data(forKey: bookmarkKey) else { return }
        do {
            var isStale = false
            let url = try URL(
                resolvingBookmarkData: data,
                options: [.withoutUI],
                relativeTo: nil,
                bookmarkDataIsStale: &isStale
            )
            activate(url)
            if isStale {
                let refreshed = try url.bookmarkData(
                    options: [.minimalBookmark],
                    includingResourceValuesForKeys: nil,
                    relativeTo: nil
                )
                UserDefaults.standard.set(refreshed, forKey: bookmarkKey)
            }
        } catch {
            UserDefaults.standard.removeObject(forKey: bookmarkKey)
            errorMessage = MobileVaultError.invalidBookmark.localizedDescription
        }
    }

    private func activate(_ url: URL) {
        if hasSecurityScope {
            vaultURL?.stopAccessingSecurityScopedResource()
        }
        let standardized = url.standardizedFileURL
        hasSecurityScope = standardized.startAccessingSecurityScopedResource()
        vaultURL = standardized
    }
}
