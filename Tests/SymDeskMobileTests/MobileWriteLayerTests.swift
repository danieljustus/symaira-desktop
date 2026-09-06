import CryptoKit
import Foundation
import XCTest
@testable import SymDeskMobile

/// Tests for the iOS write layer: outbox persistence, precondition/conflict
/// behaviour in both connection modes and frontmatter round-tripping.
final class MobileWriteLayerTests: XCTestCase {

    private var tempDirectory: URL!

    override func setUpWithError() throws {
        tempDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("OutboxTests-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: tempDirectory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: tempDirectory)
    }

    private func makeOutbox() throws -> MobileOutbox {
        try MobileOutbox(directory: tempDirectory.appendingPathComponent("outbox", isDirectory: true))
    }

    // MARK: - Outbox persistence

    func testOutboxSurvivesRecreation() async throws {
        let entry = MobileOutboxEntry(
            kind: .createNote,
            path: "inbox/hello.md",
            content: "---\ntitle: \"Hello\"\n---\n\nbody"
        )
        let outbox = try makeOutbox()
        try await outbox.enqueue(entry)
        try await outbox.storePayload(Data("payload".utf8), for: entry.id)

        // Simulate force-quit: a fresh outbox over the same directory must
        // see the entry and its payload.
        let reloaded = try makeOutbox()
        let entries = await reloaded.all
        XCTAssertEqual(entries.count, 1)
        XCTAssertEqual(entries.first?.path, "inbox/hello.md")
        XCTAssertEqual(entries.first?.state, .queued)
        let payload = try await reloaded.loadPayload(for: entry.id)
        XCTAssertEqual(String(data: payload, encoding: .utf8), "payload")
    }

    func testOutboxUpdateTransitionsState() async throws {
        let outbox = try makeOutbox()
        var entry = MobileOutboxEntry(kind: .updateNote, path: "a.md", content: "x")
        try await outbox.enqueue(entry)

        entry.state = .failed
        entry.lastError = "Server error 403: access denied"
        try await outbox.update(entry)

        let reloaded = try makeOutbox()
        let stored = await reloaded.all.first
        XCTAssertEqual(stored?.state, .failed)
        XCTAssertEqual(stored?.lastError, "Server error 403: access denied")
    }

    func testOutboxRemoveDropsEntryAndPayload() async throws {
        let outbox = try makeOutbox()
        let entry = MobileOutboxEntry(kind: .uploadOriginal, path: "scan.pdf", originalData: Data("pdf".utf8))
        try await outbox.enqueue(entry)
        try await outbox.storePayload(Data("pdf".utf8), for: entry.id)

        try await outbox.remove(id: entry.id)
        let reloaded = try makeOutbox()
        let remaining = await reloaded.all
        XCTAssertTrue(remaining.isEmpty)
    }

    // MARK: - Frontmatter round-trip (contract-v6 compatible)

    func testNoteDocumentRoundTripsThroughParser() throws {
        let root = URL(fileURLWithPath: "/tmp/SymDeskMobileVault", isDirectory: true)
        let created = Date(timeIntervalSince1970: 1_752_600_000)
        let document = MobileNoteWriter.noteDocument(title: "Kassenbon", body: "## Einkauf\n\nMilch", createdAt: created)
        let noteURL = root.appendingPathComponent("Kassenbon.md")

        let note = try MobileVaultParser.parse(
            data: Data(document.utf8),
            fileURL: noteURL,
            root: root,
            modifiedAt: created
        )

        XCTAssertEqual(note.title, "Kassenbon")
        XCTAssertEqual(note.body, "## Einkauf\n\nMilch")
        XCTAssertTrue(note.tags.isEmpty)
        // The parser must see the same searchable content — nothing lost.
        XCTAssertTrue(note.searchText.contains("kassenbon"))
        XCTAssertTrue(note.searchText.contains("milch"))
    }

    func testFilenameMatchesDesktopNoteNew() {
        XCTAssertEqual(MobileNoteWriter.filename(for: "July invoice"), "July_invoice.md")
        XCTAssertEqual(MobileNoteWriter.filename(for: "  "), "Note.md")
        XCTAssertEqual(MobileNoteWriter.filename(for: "a/b"), "a_b.md")
    }

    func testConflictFilenameMatchesDesktopConvention() {
        XCTAssertEqual(
            MobileNoteWriter.conflictFilename(for: "inbox/invoice.md"),
            "inbox/invoice conflicted copy.md"
        )
        XCTAssertEqual(
            MobileNoteWriter.conflictFilename(for: "note.txt"),
            "note conflicted copy.txt"
        )
    }

    // MARK: - Files adapter: precondition + conflict

    func testFilesAdapterCreateWritesCoordinatedNote() async throws {
        let vaultRoot = tempDirectory.appendingPathComponent("vault", isDirectory: true)
        try FileManager.default.createDirectory(at: vaultRoot, withIntermediateDirectories: true)

        let adapter = MobileFilesWriteAdapter(vaultRoot: vaultRoot)
        let entry = MobileOutboxEntry(
            kind: .createNote,
            path: "hello.md",
            content: "---\ntitle: \"Hello\"\n---\n\nworld"
        )

        try await adapter.apply(entry)

        let written = try String(contentsOf: vaultRoot.appendingPathComponent("hello.md"), encoding: .utf8)
        XCTAssertEqual(written, "---\ntitle: \"Hello\"\n---\n\nworld")
    }

    func testFilesAdapterCreateConflictPreservesBoth() async throws {
        let vaultRoot = tempDirectory.appendingPathComponent("vault", isDirectory: true)
        try FileManager.default.createDirectory(at: vaultRoot, withIntermediateDirectories: true)
        try Data("---\ntitle: Existing\n---\n\nremote".utf8)
            .write(to: vaultRoot.appendingPathComponent("hello.md"))

        let adapter = MobileFilesWriteAdapter(vaultRoot: vaultRoot)
        let entry = MobileOutboxEntry(
            kind: .createNote,
            path: "hello.md",
            content: "---\ntitle: Hello\n---\n\nlocal"
        )

        do {
            try await adapter.apply(entry)
            XCTFail("create over an existing file must conflict")
        } catch let error as MobileWriteError {
            guard case .conflict(let preservedAt, _) = error else {
                return XCTFail("expected conflict, got \(error)")
            }
            XCTAssertEqual(preservedAt, "hello conflicted copy.md")
        }

        // Remote version untouched, local version preserved as sibling.
        let original = try String(contentsOf: vaultRoot.appendingPathComponent("hello.md"), encoding: .utf8)
        XCTAssertEqual(original, "---\ntitle: Existing\n---\n\nremote")
        let sibling = try String(contentsOf: vaultRoot.appendingPathComponent("hello conflicted copy.md"), encoding: .utf8)
        XCTAssertEqual(sibling, "---\ntitle: Hello\n---\n\nlocal")
    }

    func testFilesAdapterUpdateWithStalePreconditionConflicts() async throws {
        let vaultRoot = tempDirectory.appendingPathComponent("vault", isDirectory: true)
        try FileManager.default.createDirectory(at: vaultRoot, withIntermediateDirectories: true)
        let target = vaultRoot.appendingPathComponent("note.md")
        try Data("remote-change".utf8).write(to: target)
        let staleDate = Date(timeIntervalSince1970: 100)

        let adapter = MobileFilesWriteAdapter(vaultRoot: vaultRoot)
        let entry = MobileOutboxEntry(
            kind: .updateNote,
            path: "note.md",
            content: "local-edit",
            precondition: MobileWritePrecondition(modifiedAt: staleDate, size: 5)
        )

        do {
            try await adapter.apply(entry)
            XCTFail("update with stale precondition must conflict")
        } catch let error as MobileWriteError {
            guard case .conflict = error else {
                return XCTFail("expected conflict, got \(error)")
            }
        }

        let original = try String(contentsOf: target, encoding: .utf8)
        XCTAssertEqual(original, "remote-change")
        let sibling = try String(contentsOf: vaultRoot.appendingPathComponent("note conflicted copy.md"), encoding: .utf8)
        XCTAssertEqual(sibling, "local-edit")
    }

    func testFilesAdapterUpdateWithMatchingPreconditionApplies() async throws {
        let vaultRoot = tempDirectory.appendingPathComponent("vault", isDirectory: true)
        try FileManager.default.createDirectory(at: vaultRoot, withIntermediateDirectories: true)
        let target = vaultRoot.appendingPathComponent("note.md")
        try Data("old".utf8).write(to: target)
        let values = try target.resourceValues(forKeys: [.contentModificationDateKey, .fileSizeKey])

        let adapter = MobileFilesWriteAdapter(vaultRoot: vaultRoot)
        let entry = MobileOutboxEntry(
            kind: .updateNote,
            path: "note.md",
            content: "new",
            precondition: MobileWritePrecondition(
                modifiedAt: values.contentModificationDate,
                size: values.fileSize
            )
        )

        try await adapter.apply(entry)
        let written = try String(contentsOf: target, encoding: .utf8)
        XCTAssertEqual(written, "new")
    }

    // MARK: - Server adapter: precondition + conflict via stub transport

    func testServerAdapterUpdateWithChangedRemoteConflicts() async throws {
        // URLProtocol stub: the live file has different content than the
        // precondition hash the phone recorded.
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [StubURLProtocol.self]
        let session = URLSession(configuration: configuration)

        let connection = MobileServerConnection(
            url: URL(string: "https://symdesk.example.test")!,
            token: String(repeating: "a", count: 32)
        )
        StubURLProtocol.handler = { request in
            let path = request.url?.queryParameters["path"] ?? ""
            if request.httpMethod == "GET" {
                if path == "note.md" {
                    // The remote version differs from what the phone saw.
                    let body = Data("remote-version".utf8)
                    return (HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!, body)
                }
                return (HTTPURLResponse(url: request.url!, statusCode: 404, httpVersion: nil, headerFields: nil)!, Data())
            }
            if request.httpMethod == "PUT" {
                let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
                return (response, Data("{\"status\":\"updated\"}".utf8))
            }
            let response = HTTPURLResponse(url: request.url!, statusCode: 405, httpVersion: nil, headerFields: nil)!
            return (response, Data())
        }

        let adapter = MobileServerWriteAdapter(connection: connection, session: session)
        let phoneSaw = MobileServerWriteAdapter.sha256(Data("phone-saw-this".utf8))
        let entry = MobileOutboxEntry(
            kind: .updateNote,
            path: "note.md",
            content: "phone-edit",
            precondition: MobileWritePrecondition(etag: phoneSaw)
        )

        do {
            try await adapter.apply(entry)
            XCTFail("update over a changed remote must conflict")
        } catch let error as MobileWriteError {
            guard case .conflict(let preservedAt, _) = error else {
                return XCTFail("expected conflict, got \(error)")
            }
            XCTAssertEqual(preservedAt, "note conflicted copy.md")
        }
    }

    func testServerAdapterRejectedWriteSurfacesReason() async throws {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [StubURLProtocol.self]
        let session = URLSession(configuration: configuration)

        let connection = MobileServerConnection(
            url: URL(string: "https://symdesk.example.test")!,
            token: String(repeating: "a", count: 32)
        )
        StubURLProtocol.handler = { request in
            if request.httpMethod == "GET" {
                return (HTTPURLResponse(url: request.url!, statusCode: 404, httpVersion: nil, headerFields: nil)!, Data())
            }
            // PUT rejected: permission denied.
            let response = HTTPURLResponse(url: request.url!, statusCode: 403, httpVersion: nil, headerFields: nil)!
            return (response, Data("{\"error\":\"access denied\"}".utf8))
        }

        let adapter = MobileServerWriteAdapter(connection: connection, session: session)
        let entry = MobileOutboxEntry(
            kind: .createNote,
            path: "note.md",
            content: "---\ntitle: N\n---\n\nbody"
        )

        do {
            try await adapter.apply(entry)
            XCTFail("403 must reject")
        } catch let error as MobileWriteError {
            guard case .rejected(let status, let reason) = error else {
                return XCTFail("expected rejection, got \(error)")
            }
            XCTAssertEqual(status, 403)
            XCTAssertEqual(reason, "access denied")
        }
    }

    // MARK: - Coordinator: drain, retry, mode switching

    func testCoordinatorDrainAppliesAndRemovesQueuedEntry() async throws {
        let outbox = try makeOutbox()
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        let vaultRoot = tempDirectory.appendingPathComponent("vault", isDirectory: true)
        try FileManager.default.createDirectory(at: vaultRoot, withIntermediateDirectories: true)
        await coordinator.setMode(MobileFilesWriteAdapter(vaultRoot: vaultRoot))

        let entry = MobileOutboxEntry(kind: .createNote, path: "drained.md", content: "content")
        try await coordinator.enqueue(entry)

        // Give the async drain a moment to run.
        for _ in 0..<250 {
            if try FileManager.default.fileExists(atPath: vaultRoot.appendingPathComponent("drained.md").path) {
                break
            }
            try? await Task.sleep(for: .milliseconds(20))
        }

        XCTAssertTrue(try String(contentsOf: vaultRoot.appendingPathComponent("drained.md"), encoding: .utf8) == "content")
        let remaining = await coordinator.entries()
        XCTAssertTrue(remaining.isEmpty, "applied entries must leave the queue")
    }

    func testCoordinatorNetworkFailureKeepsEntryQueued() async throws {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [StubURLProtocol.self]
        let session = URLSession(configuration: configuration)

        let connection = MobileServerConnection(
            url: URL(string: "https://symdesk.example.test")!,
            token: String(repeating: "a", count: 32)
        )
        // Every request fails like a missing connection.
        StubURLProtocol.handler = { request in
            throw URLError(.notConnectedToInternet)
        }

        let outbox = try makeOutbox()
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        await coordinator.setMode(MobileServerWriteAdapter(connection: connection, session: session))

        let entry = MobileOutboxEntry(kind: .createNote, path: "n.md", content: "x")
        try await coordinator.enqueue(entry)

        for _ in 0..<250 {
            if await coordinator.entries().first?.state == .queued,
               await coordinator.entries().first?.attempts ?? 0 > 0 {
                break
            }
            try? await Task.sleep(for: .milliseconds(20))
        }

        let stored = await coordinator.entries().first
        XCTAssertEqual(stored?.state, .queued, "network failures must not mark the entry failed")
        XCTAssertGreaterThan(stored?.attempts ?? 0, 0)
        XCTAssertNotNil(stored?.nextRetryAt, "backoff must schedule a retry")
        XCTAssertNotNil(stored?.lastError)
    }

    func testCoordinatorRetryClearsFailedState() async throws {
        let outbox = try makeOutbox()
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        let vaultRoot = tempDirectory.appendingPathComponent("vault", isDirectory: true)
        try FileManager.default.createDirectory(at: vaultRoot, withIntermediateDirectories: true)

        // No mode set → drain can't apply; entry stays queued.
        let entry = MobileOutboxEntry(kind: .createNote, path: "a.md", content: "x")
        try await coordinator.enqueue(entry)

        // Force the entry into the failed state, then retry it.
        var failed = await coordinator.entries().first!
        failed.state = .failed
        failed.lastError = "boom"
        try await outbox.update(failed)
        try await coordinator.retry(id: failed.id)

        let afterRetry = await coordinator.entries().first
        XCTAssertEqual(afterRetry?.state, .queued)
        XCTAssertNil(afterRetry?.lastError)
    }

    func testCoordinatorWithoutModeKeepsEntryQueued() async throws {
        let outbox = try makeOutbox()
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        let entry = MobileOutboxEntry(kind: .createNote, path: "a.md", content: "x")
        try await coordinator.enqueue(entry)

        try? await Task.sleep(for: .milliseconds(100))
        let stored = await coordinator.entries().first
        XCTAssertEqual(stored?.state, .queued)
        let count = await coordinator.entries().count
        XCTAssertEqual(count, 1)
    }
}

// MARK: - URLProtocol stub

private final class StubURLProtocol: URLProtocol {
    nonisolated(unsafe) static var handler: (@Sendable (URLRequest) throws -> (HTTPURLResponse, Data))?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let handler = Self.handler else {
            client?.urlProtocol(self, didFailWithError: URLError(.badServerResponse))
            return
        }
        do {
            let (response, data) = try handler(request)
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}
}

private extension URL {
    var queryParameters: [String: String] {
        guard let components = URLComponents(url: self, resolvingAgainstBaseURL: false),
              let items = components.queryItems else { return [:] }
        return Dictionary(uniqueKeysWithValues: items.compactMap { item in
            item.value.map { (item.name, $0) }
        })
    }
}
