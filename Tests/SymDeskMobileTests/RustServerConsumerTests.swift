import CryptoKit
import Foundation
import XCTest
@testable import SymDeskMobile

/// SWIFT-002 (docs/rust-port/contract-matrix.md): the iOS server-mode
/// consumers — `MobileRemoteClient`, `MobileServerWriteAdapter`, the durable
/// outbox/coordinator, the share-extension inbox and the server AI client —
/// driven over real HTTP against a Rust `symdesk serve` process. No
/// `URLProtocol` stubs: every request leaves the simulator process.
///
/// Gated on `SYMDESK_CONSUMER_SERVER_URL` + `SYMDESK_CONSUMER_SERVER_TOKEN`
/// (pass with the `TEST_RUNNER_` prefix). `scripts/rust-port/swift-consumer.sh`
/// starts the server on a disposable vault seeded from
/// `testdata/swift-consumer/vault`, and fails when the xcresult shows zero
/// executed tests, so a skipped run is never reported as a pass.
final class RustServerConsumerTests: XCTestCase {
    private var connection: MobileServerConnection!
    private var tempDirectory: URL!
    private var runID = ""

    override func setUpWithError() throws {
        let environment = ProcessInfo.processInfo.environment
        guard let rawURL = environment["SYMDESK_CONSUMER_SERVER_URL"], !rawURL.isEmpty,
              let token = environment["SYMDESK_CONSUMER_SERVER_TOKEN"], !token.isEmpty else {
            throw XCTSkip("SYMDESK_CONSUMER_SERVER_URL/TOKEN are not set; use scripts/rust-port/swift-consumer.sh")
        }
        let url = try XCTUnwrap(MobileServerConfig.normalizedURL(rawURL), "invalid server URL \(rawURL)")
        connection = MobileServerConnection(url: url, token: token)
        runID = String(UUID().uuidString.prefix(8)).lowercased()
        tempDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("RustServerConsumer-\(runID)", isDirectory: true)
        try FileManager.default.createDirectory(at: tempDirectory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        if let tempDirectory { try? FileManager.default.removeItem(at: tempDirectory) }
    }

    // MARK: - Status / auth

    func testStatusAcceptsBearerToken() async throws {
        try await MobileRemoteClient(connection: connection).status()
    }

    func testWrongTokenIsRejectedWithAuthenticationError() async throws {
        let wrong = MobileServerConnection(url: connection.url, token: String(repeating: "x", count: 40))
        let client = MobileRemoteClient(connection: wrong)

        do {
            try await client.status()
            XCTFail("a wrong bearer token must not authenticate")
        } catch let MobileServerError.server(status, message) {
            XCTAssertEqual(status, 401)
            XCTAssertEqual(message, "authentication required")
        }

        do {
            _ = try await client.snapshot(ifNoneMatch: nil)
            XCTFail("snapshot must require authentication")
        } catch let MobileServerError.server(status, _) {
            XCTAssertEqual(status, 401)
        }
    }

    // MARK: - Snapshot, ETag, cached originals

    func testSnapshotDecodesSeedNotesAndHonoursETag() async throws {
        let client = MobileRemoteClient(connection: connection)

        guard case let .updated(snapshot, etag) = try await client.snapshot(ifNoneMatch: nil) else {
            return XCTFail("first snapshot cannot be 304")
        }
        XCTAssertEqual(snapshot.skippedFiles, 0)
        let etagValue = try XCTUnwrap(etag)
        XCTAssertFalse(etagValue.isEmpty)

        let alpha = try XCTUnwrap(snapshot.notes.first { $0.path == "alpha.md" })
        XCTAssertEqual(alpha.title, "Alpha Plan")
        XCTAssertEqual(alpha.tags, ["planning", "rust-port"])
        XCTAssertEqual(alpha.status, "open")
        XCTAssertEqual(alpha.created, "2026-01-02T03:04:05Z")
        XCTAssertTrue(alpha.body.contains("Zebra migration strategy"))
        XCTAssertTrue(alpha.isDocument, "a status makes a note a document in the iOS list")

        let beta = try XCTUnwrap(snapshot.notes.first { $0.path == "projects/beta.md" })
        XCTAssertEqual(beta.title, "Beta Notes")
        XCTAssertEqual(beta.tags, [])

        let invoice = try XCTUnwrap(snapshot.notes.first { $0.path == "documents/invoice.md" })
        XCTAssertTrue(invoice.isDocument)
        XCTAssertEqual(invoice.documentType, "invoice")
        XCTAssertEqual(invoice.correspondent, "ACME GmbH")
        XCTAssertEqual(invoice.dueDate, "2026-08-01")
        XCTAssertEqual(invoice.confidence, 91)
        XCTAssertEqual(invoice.attachmentReferences, ["files/receipt.pdf"])

        // Conditional refresh: unchanged vault costs a 304.
        guard case .unchanged = try await client.snapshot(ifNoneMatch: etagValue) else {
            return XCTFail("matching If-None-Match must yield 304 / .unchanged")
        }
        // A stale ETag must produce a full snapshot again.
        guard case .updated = try await client.snapshot(ifNoneMatch: "\"stale\"") else {
            return XCTFail("a non-matching If-None-Match must return a full snapshot")
        }
    }

    func testSnapshotETagChangesAfterRemoteWrite() async throws {
        let client = MobileRemoteClient(connection: connection)
        guard case let .updated(_, firstTag) = try await client.snapshot(ifNoneMatch: nil),
              let etag = firstTag else { return XCTFail("missing first snapshot/ETag") }

        let path = "consumer-\(runID)/snapshot-change.md"
        try await probe.putMarkdown(path: path, content: MobileNoteWriter.noteDocument(title: "Snapshot Change \(runID)", body: "appears"))

        // The server refreshes its cached snapshot asynchronously (watcher +
        // metadata pass); poll like the app's pull-to-refresh would.
        var found = false
        var latest = etag
        for _ in 0..<60 where !found {
            if case let .updated(snapshot, newTag) = try await client.snapshot(ifNoneMatch: latest) {
                latest = newTag ?? latest
                found = snapshot.notes.contains { $0.path == path && $0.title == "Snapshot Change \(runID)" }
            }
            if !found { try await Task.sleep(for: .milliseconds(500)) }
        }
        XCTAssertTrue(found, "the written note never appeared in a refreshed snapshot")
        XCTAssertNotEqual(latest, etag)
    }

    func testCachedAttachmentFetchesOriginalFromFilesEndpoint() async throws {
        let client = MobileRemoteClient(connection: connection)
        guard case let .updated(snapshot, _) = try await client.snapshot(ifNoneMatch: nil) else {
            return XCTFail("missing snapshot")
        }
        let invoice = try XCTUnwrap(snapshot.notes.first { $0.path == "documents/invoice.md" })

        let cached = try await XCTUnwrapAsync(await client.cachedAttachment(for: invoice))
        let bytes = try Data(contentsOf: cached)
        XCTAssertEqual(
            String(decoding: bytes, as: UTF8.self),
            "%PDF-1.4\n% symdesk swift consumer fixture (not a real PDF)\n"
        )
        XCTAssertTrue(cached.lastPathComponent.hasSuffix("-receipt.pdf"))
    }

    // MARK: - Outbox: create / update / conflict / rejection

    func testOutboxCreateUpdateAndPreconditionConflictOverHTTP() async throws {
        let (coordinator, _) = try await makeCoordinator()
        let path = "consumer-\(runID)/outbox-note.md"
        let created = MobileNoteWriter.noteDocument(title: "Outbox \(runID)", body: "first body")

        // create
        try await coordinator.enqueue(MobileOutboxEntry(kind: .createNote, path: path, content: created))
        try await waitForEmptyQueue(coordinator)
        let afterCreate = try await probe.getMarkdown(path: path)
        XCTAssertEqual(afterCreate, created)

        // update with the precondition the phone recorded from its snapshot
        let updated = MobileNoteWriter.noteDocument(title: "Outbox \(runID)", body: "second body")
        try await coordinator.enqueue(MobileOutboxEntry(
            kind: .updateNote, path: path, content: updated,
            precondition: MobileWritePrecondition(etag: MobileServerWriteAdapter.sha256(Data(created.utf8)))
        ))
        try await waitForEmptyQueue(coordinator)
        let afterUpdate = try await probe.getMarkdown(path: path)
        XCTAssertEqual(afterUpdate, updated)

        // stale precondition → conflicted copy, remote version wins
        let staleEdit = MobileNoteWriter.noteDocument(title: "Outbox \(runID)", body: "edit made offline")
        try await coordinator.enqueue(MobileOutboxEntry(
            kind: .updateNote, path: path, content: staleEdit,
            precondition: MobileWritePrecondition(etag: MobileServerWriteAdapter.sha256(Data(created.utf8)))
        ))
        let failed = try await waitForFailedEntry(coordinator)
        XCTAssertEqual(failed.conflictPath, "consumer-\(runID)/outbox-note conflicted copy.md")
        XCTAssertTrue(failed.lastError?.contains("changed on the server") == true, failed.lastError ?? "nil")
        let afterConflict = try await probe.getMarkdown(path: path)
        XCTAssertEqual(afterConflict, updated, "remote version must be untouched by a conflicting edit")
        let preserved = try await probe.getMarkdown(path: try XCTUnwrap(failed.conflictPath))
        XCTAssertEqual(preserved, staleEdit, "the losing local version is preserved byte-for-byte")
    }

    func testCreateOverExistingNotePreservesConflictCopy() async throws {
        let (coordinator, _) = try await makeCoordinator()
        let path = "consumer-\(runID)/existing.md"
        let remote = MobileNoteWriter.noteDocument(title: "Existing \(runID)", body: "remote body")
        try await probe.putMarkdown(path: path, content: remote)

        let local = MobileNoteWriter.noteDocument(title: "Existing \(runID)", body: "local body")
        try await coordinator.enqueue(MobileOutboxEntry(kind: .createNote, path: path, content: local))
        let failed = try await waitForFailedEntry(coordinator)

        XCTAssertEqual(failed.conflictPath, "consumer-\(runID)/existing conflicted copy.md")
        let remoteAfter = try await probe.getMarkdown(path: path)
        XCTAssertEqual(remoteAfter, remote)
        let preserved = try await probe.getMarkdown(path: "consumer-\(runID)/existing conflicted copy.md")
        XCTAssertEqual(preserved, local)
    }

    func testServerRejectionSurfacesStatusAndStaysFailedUntilRetried() async throws {
        let (coordinator, _) = try await makeCoordinator()
        // Only vault-relative Markdown may be written: the Rust server answers 400.
        try await coordinator.enqueue(MobileOutboxEntry(
            kind: .createNote, path: "consumer-\(runID)/not-markdown.txt", content: "x"
        ))
        let failed = try await waitForFailedEntry(coordinator)
        XCTAssertEqual(failed.state, .failed)
        XCTAssertTrue(failed.lastError?.contains("(400)") == true, failed.lastError ?? "nil")

        // retry puts it back in the queue, and it fails again for the same reason
        try await coordinator.retry(id: failed.id)
        let again = try await waitForFailedEntry(coordinator)
        XCTAssertEqual(again.id, failed.id)
        XCTAssertTrue(again.lastError?.contains("(400)") == true)
    }

    func testWrongTokenWriteFailsWithAuthenticationStatus() async throws {
        let wrong = MobileServerConnection(url: connection.url, token: String(repeating: "z", count: 40))
        let outbox = try MobileOutbox(directory: tempDirectory.appendingPathComponent("outbox-wrong"))
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        await coordinator.setMode(MobileServerWriteAdapter(connection: wrong))

        try await coordinator.enqueue(MobileOutboxEntry(
            kind: .createNote, path: "consumer-\(runID)/unauthorized.md", content: "x"
        ))
        let failed = try await waitForFailedEntry(coordinator)
        XCTAssertTrue(failed.lastError?.contains("(401)") == true, failed.lastError ?? "nil")
        // nothing was written
        do {
            _ = try await probe.getMarkdown(path: "consumer-\(runID)/unauthorized.md")
            XCTFail("an unauthenticated write must not reach the vault")
        } catch let error as ProbeError {
            XCTAssertEqual(error.status, 404)
        }
    }

    func testOfflineEntryBacksOffAndAppliesWhenServerBecomesReachable() async throws {
        let unreachable = MobileServerConnection(url: URL(string: "http://127.0.0.1:1")!, token: connection.token)
        let (coordinator, outbox) = try await makeCoordinator(connection: unreachable, name: "outbox-offline")
        let path = "consumer-\(runID)/offline.md"
        let content = MobileNoteWriter.noteDocument(title: "Offline \(runID)", body: "queued while offline")

        try await coordinator.enqueue(MobileOutboxEntry(kind: .createNote, path: path, content: content))
        var queued: MobileOutboxEntry?
        for _ in 0..<200 {
            queued = await coordinator.entries().first
            if (queued?.attempts ?? 0) > 0 { break }
            try await Task.sleep(for: .milliseconds(50))
        }
        let waiting = try XCTUnwrap(queued)
        XCTAssertEqual(waiting.state, .queued, "network failures must keep the entry queued, not failed")
        XCTAssertGreaterThan(waiting.attempts, 0)
        XCTAssertNotNil(waiting.nextRetryAt)

        // The entry survives a relaunch (fresh outbox over the same directory).
        let reloaded = try MobileOutbox(directory: tempDirectory.appendingPathComponent("outbox-offline"))
        let persisted = await reloaded.all
        XCTAssertEqual(persisted.map(\.path), [path])
        _ = outbox

        // Connectivity returns: the entry is still inside its backoff window,
        // so it applies once the window has passed and a drain is triggered
        // (reload / NWPath satisfied in the app).
        await coordinator.setMode(MobileServerWriteAdapter(connection: connection))
        try await Task.sleep(for: .milliseconds(2_500))
        await coordinator.drain()
        try await waitForEmptyQueue(coordinator)
        let remote = try await probe.getMarkdown(path: path)
        XCTAssertEqual(remote, content)
    }

    // MARK: - Uploads, share extension inbox

    func testUploadOriginalIsIngestedByTheServer() async throws {
        let (coordinator, _) = try await makeCoordinator()
        let filename = "upload-\(runID).pdf"
        let payload = Data("%PDF-1.4 upload \(runID)".utf8)

        try await coordinator.enqueue(MobileOutboxEntry(
            kind: .uploadOriginal, path: filename, originalData: payload, originalFilename: filename
        ))
        try await waitForEmptyQueue(coordinator)

        let job = try await probe.waitForJob(originalName: filename)
        XCTAssertEqual(job["original_name"] as? String, filename)
        XCTAssertEqual(job["status"] as? String, "pending")
        XCTAssertEqual(job["schema_version"] as? Int, 1)
    }

    func testShareExtensionInboxItemsReachTheServerThroughTheSameClient() async throws {
        let (coordinator, _) = try await makeCoordinator()
        let inbox = tempDirectory.appendingPathComponent("ShareInbox", isDirectory: true)
        try FileManager.default.createDirectory(at: inbox, withIntermediateDirectories: true)
        // The extension drops files named `share-<name>` into the App Group inbox.
        try Data("shared text \(runID)".utf8).write(to: inbox.appendingPathComponent("share-shared \(runID).txt"))

        let shareInbox = MobileShareInbox(coordinator: coordinator, inboxURL: inbox)
        XCTAssertEqual(shareInbox.pendingCount(), 1)
        await shareInbox.drain()
        try await waitForEmptyQueue(coordinator)
        XCTAssertEqual(shareInbox.pendingCount(), 0, "inbox copy is removed after a successful enqueue")

        let job = try await probe.waitForJob(originalName: "shared_\(runID).txt")
        XCTAssertEqual(job["status"] as? String, "pending")
    }

    // MARK: - Store: connect / reload / write, end to end

    @MainActor
    func testVaultStoreConnectsReloadsAndWritesThroughOutbox() async throws {
        defer { MobileServerConfig.reset() }
        let outbox = try MobileOutbox(directory: tempDirectory.appendingPathComponent("store-outbox"))
        let store = MobileVaultStore(
            outbox: outbox,
            cacheURL: tempDirectory.appendingPathComponent("cache.json"),
            searchIndex: MobileSearchIndex(fileURL: tempDirectory.appendingPathComponent("search-index.json"))
        )

        try await store.connectServer(url: connection.url.absoluteString, token: connection.token)
        XCTAssertTrue(store.isRemote)
        XCTAssertNil(store.errorMessage)
        XCTAssertTrue(store.notes.contains { $0.path == "alpha.md" && $0.title == "Alpha Plan" })

        let title = "Store Note \(runID)"
        let path = try await store.enqueueCreateNote(title: title, body: "created through the store", folder: "consumer-\(runID)")
        XCTAssertEqual(path, "consumer-\(runID)/Store_Note_\(runID).md")

        var written: String?
        for _ in 0..<100 where written == nil {
            written = try? await probe.getMarkdown(path: path)
            if written == nil { try await Task.sleep(for: .milliseconds(100)) }
        }
        let document = try XCTUnwrap(written)
        XCTAssertTrue(document.hasPrefix("---\ntitle: \"\(title)\"\ncreated: \""), document)
        XCTAssertTrue(document.contains("\ntags: []\n---\n\ncreated through the store"), document)
    }

    // MARK: - Server AI (NDJSON)

    func testServerAIAskAndTransformStreamNDJSONEvents() async throws {
        let client = MobileAIClient(connection: connection, defaults: UserDefaults(suiteName: "rust-consumer-\(runID)")!)

        let askEvents = EventBox()
        try await client.ask(query: "zebra") { askEvents.append($0) }
        let ask = askEvents.snapshot()
        XCTAssertEqual(ask.last?.type, .done)
        XCTAssertTrue(ask.contains { $0.type == .citation && $0.path == "alpha.md" && $0.title == "Alpha Plan" })
        XCTAssertTrue(ask.contains { $0.type == .tool && $0.toolName == "search" && $0.status == "done" })
        XCTAssertTrue(ask.contains { $0.type == .answer && ($0.text ?? "").contains("AI feature not configured") })

        let transformEvents = EventBox()
        try await client.transform(text: "hello world", intent: "summarize") { transformEvents.append($0) }
        let transform = transformEvents.snapshot()
        XCTAssertEqual(transform.last?.type, .done)
        XCTAssertTrue(transform.contains { $0.type == .answer && ($0.text ?? "").contains("AI feature not configured") })
    }

    // MARK: - Helpers

    private final class EventBox: @unchecked Sendable {
        private let lock = NSLock()
        private var events: [MobileAIEvent] = []
        func append(_ event: MobileAIEvent) { lock.lock(); events.append(event); lock.unlock() }
        func snapshot() -> [MobileAIEvent] { lock.lock(); defer { lock.unlock() }; return events }
    }

    private func makeCoordinator(
        connection override: MobileServerConnection? = nil,
        name: String = "outbox"
    ) async throws -> (MobileWriteCoordinator, MobileOutbox) {
        let outbox = try MobileOutbox(directory: tempDirectory.appendingPathComponent(name, isDirectory: true))
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        let target = override ?? connection!
        await coordinator.setMode(MobileServerWriteAdapter(connection: target))
        return (coordinator, outbox)
    }

    private func waitForEmptyQueue(_ coordinator: MobileWriteCoordinator, timeout: Int = 30) async throws {
        for _ in 0..<(timeout * 20) {
            if await coordinator.entries().isEmpty { return }
            try await Task.sleep(for: .milliseconds(50))
        }
        let left = await coordinator.entries().map { "\($0.path):\($0.state.rawValue):\($0.lastError ?? "-")" }
        XCTFail("outbox did not drain: \(left)")
    }

    private func waitForFailedEntry(_ coordinator: MobileWriteCoordinator, timeout: Int = 30) async throws -> MobileOutboxEntry {
        for _ in 0..<(timeout * 20) {
            if let failed = await coordinator.failedEntries().first { return failed }
            try await Task.sleep(for: .milliseconds(50))
        }
        let all = await coordinator.entries().map { "\($0.path):\($0.state.rawValue):\($0.lastError ?? "-")" }
        XCTFail("no failed entry appeared (entries: \(all))")
        throw ProbeError(status: -2)
    }

    private var probe: Probe { Probe(connection: connection) }

    private func XCTUnwrapAsync<T>(_ expression: @autoclosure () async throws -> T?, file: StaticString = #filePath,
                                   line: UInt = #line) async throws -> T {
        guard let value = try await expression() else {
            XCTFail("expected a non-nil value", file: file, line: line)
            throw ProbeError(status: -1)
        }
        return value
    }
}

/// Plain HTTP access to the server under test (the vault's files and ingest
/// queue), independent of the production client being exercised.
private struct ProbeError: Error { let status: Int }

private struct Probe: Sendable {
    let connection: MobileServerConnection

    func request(path: String, method: String = "GET", query: [URLQueryItem] = [], body: Data? = nil,
                         contentType: String? = nil) async throws -> (Data, Int) {
        var components = URLComponents(url: connection.url.appendingPathComponent(path), resolvingAgainstBaseURL: false)!
        components.queryItems = query.isEmpty ? nil : query
        var request = URLRequest(url: components.url!)
        request.httpMethod = method
        request.setValue("Bearer \(connection.token)", forHTTPHeaderField: "Authorization")
        if let contentType { request.setValue(contentType, forHTTPHeaderField: "Content-Type") }
        request.httpBody = body
        let (data, response) = try await URLSession.shared.data(for: request)
        return (data, (response as! HTTPURLResponse).statusCode)
    }

    func getMarkdown(path: String) async throws -> String {
        let (data, status) = try await request(path: "/api/v1/files", query: [URLQueryItem(name: "path", value: path)])
        guard status == 200 else { throw ProbeError(status: status) }
        return String(decoding: data, as: UTF8.self)
    }

    func putMarkdown(path: String, content: String) async throws {
        let (_, status) = try await request(
            path: "/api/v1/files", method: "PUT", query: [URLQueryItem(name: "path", value: path)],
            body: Data(content.utf8), contentType: "text/markdown; charset=utf-8"
        )
        guard (200..<300).contains(status) else { throw ProbeError(status: status) }
    }

    func waitForJob(originalName: String) async throws -> [String: Any] {
        for _ in 0..<100 {
            let (data, status) = try await request(path: "/api/v1/jobs")
            if status == 200 {
                let jobs: [[String: Any]]
                if let array = try JSONSerialization.jsonObject(with: data) as? [[String: Any]] {
                    jobs = array
                } else if let page = try JSONSerialization.jsonObject(with: data) as? [String: Any],
                          let array = page["jobs"] as? [[String: Any]] {
                    jobs = array
                } else {
                    jobs = []
                }
                if let job = jobs.first(where: { $0["original_name"] as? String == originalName }) { return job }
            }
            try await Task.sleep(for: .milliseconds(100))
        }
        XCTFail("no ingest job named \(originalName)")
        return [:]
    }

}
