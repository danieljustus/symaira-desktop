import Foundation
import XCTest
@testable import SymDeskMobile

/// Tests for the vault-grounded chat (#330): AIEvent wire decoding,
/// conversation persistence/deletion and the streaming client against a
/// fake NDJSON endpoint.
final class MobileChatTests: XCTestCase {

    private var tempDirectory: URL!

    override func setUpWithError() throws {
        tempDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("ChatTests-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: tempDirectory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: tempDirectory)
    }

    private func makeConversationStore() throws -> MobileConversationStore {
        try MobileConversationStore(directory: tempDirectory.appendingPathComponent("chats", isDirectory: true))
    }

    // MARK: - Wire decoding

    func testDecodesAnswerEvent() throws {
        let event = try JSONDecoder().decode(
            MobileAIEvent.self,
            from: Data(#"{"type":"answer","text":"Hallo"}"#.utf8)
        )
        XCTAssertEqual(event.type, .answer)
        XCTAssertEqual(event.text, "Hallo")
    }

    func testDecodesCitationEvent() throws {
        let event = try JSONDecoder().decode(
            MobileAIEvent.self,
            from: Data(#"{"type":"citation","path":"inbox/invoice.md","title":"Invoice","snippet":"Die Rechnung","score":0.9}"#.utf8)
        )
        XCTAssertEqual(event.type, .citation)
        XCTAssertEqual(event.path, "inbox/invoice.md")
        XCTAssertEqual(event.title, "Invoice")
        XCTAssertEqual(event.score ?? 0, 0.9, accuracy: 0.001)
    }

    func testDecodesToolAndDoneEvents() throws {
        let tool = try JSONDecoder().decode(MobileAIEvent.self, from: Data(#"{"type":"tool","tool_name":"search","status":"done"}"#.utf8))
        XCTAssertEqual(tool.type, .tool)
        XCTAssertEqual(tool.toolName, "search")

        let done = try JSONDecoder().decode(MobileAIEvent.self, from: Data(#"{"type":"done"}"#.utf8))
        XCTAssertEqual(done.type, .done)
    }

    // MARK: - Conversation persistence

    func testConversationPersistsAcrossStoreRecreation() async throws {
        let store = try makeConversationStore()
        let conversation = MobileConversation(
            id: UUID(uuidString: "00000000-0000-0000-0000-000000000001")!,
            title: "Rechnungen",
            messages: [
                MobileChatMessage(role: .user, text: "Was ist fällig?"),
                MobileChatMessage(
                    role: .assistant,
                    text: "Eine Rechnung.",
                    citations: [MobileChatCitation(path: "invoice.md", title: "Invoice", snippet: "…", score: 1)]
                )
            ]
        )
        try await store.save(conversation)

        let reloaded = try makeConversationStore()
        let loaded = try await reloaded.load(id: conversation.id)
        XCTAssertEqual(loaded?.title, "Rechnungen")
        XCTAssertEqual(loaded?.messages.count, 2)
        XCTAssertEqual(loaded?.messages[1].citations.first?.path, "invoice.md")
        XCTAssertEqual(loaded?.messages[0].role, .user)
    }

    func testConversationListingNewestFirstAndDeletion() async throws {
        let store = try makeConversationStore()
        let old = MobileConversation(id: UUID(), title: "Alt", updatedAt: Date(timeIntervalSince1970: 100))
        let recent = MobileConversation(id: UUID(), title: "Neu", updatedAt: Date(timeIntervalSince1970: 200))
        try await store.save(old)
        try await store.save(recent)

        let all = try await store.all()
        XCTAssertEqual(all.map(\.title), ["Neu", "Alt"])

        try await store.delete(id: recent.id)
        let remaining = try await store.all()
        XCTAssertEqual(remaining.map(\.title), ["Alt"])
    }

    func testDeleteAllClearsConversations() async throws {
        let store = try makeConversationStore()
        try await store.save(MobileConversation(id: UUID(), title: "A"))
        try await store.save(MobileConversation(id: UUID(), title: "B"))
        try await store.deleteAll()
        let all = try await store.all()
        XCTAssertTrue(all.isEmpty)
    }

    // MARK: - Streaming client

    func testAskStreamsEventsInOrder() async throws {
        let fixture = """
        {"type":"tool","tool_name":"search","status":"running"}
        {"type":"tool","tool_name":"search","status":"done"}
        {"type":"citation","path":"invoice.md","title":"Invoice","snippet":"Die Rechnung","score":0.9}
        {"type":"answer","text":"Erste "}
        {"type":"answer","text":"Hälfte"}
        {"type":"done"}
        """
        let session = try await withMockedAIEndpoint(statusCode: 200, body: fixture)

        let connection = MobileServerConnection(url: URL(string: "https://mock.test")!, token: String(repeating: "a", count: 32))
        let client = MobileAIClient(connection: connection, session: session)

        let collected = LockedEvents()
        try await client.ask(query: "Was ist fällig?") { event in
            collected.append(event)
        }
        let events = collected.snapshot()

        XCTAssertEqual(events.count, 6)
        XCTAssertEqual(events.first?.type, .tool)
        XCTAssertEqual(events[2].type, .citation)
        XCTAssertEqual(events[3].text, "Erste ")
        XCTAssertEqual(events[5].type, .done)

        let answers = events.filter { $0.type == .answer }.compactMap(\.text).joined()
        XCTAssertEqual(answers, "Erste Hälfte")
    }

    func testAskRejectsUnauthorized() async throws {
        let session = try await withMockedAIEndpoint(statusCode: 401, body: #"{"error":"authentication required"}"#)

        let connection = MobileServerConnection(url: URL(string: "https://mock.test")!, token: String(repeating: "a", count: 32))
        let client = MobileAIClient(connection: connection, session: session)

        do {
            try await client.ask(query: "x") { _ in }
            XCTFail("expected 401 to throw")
        } catch let error as MobileAIClient.AIError {
            guard case .server(let status, _) = error else {
                return XCTFail("expected server error, got \(error)")
            }
            XCTAssertEqual(status, 401)
        }
    }

    // MARK: - Helpers

    /// Installs a URLProtocol-backed mock AI endpoint and returns a session
    /// whose bytes(for:) streams the fixture body line by line.
    private func withMockedAIEndpoint(statusCode: Int, body: String) async throws -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [MockAIProtocol.self]
        MockAIProtocol.fixture = MockAIProtocol.Fixture(statusCode: statusCode, body: body)
        return URLSession(configuration: config)
    }
}

/// URLProtocol fixture: serves the NDJSON body with the AI endpoint's
/// headers, so `URLSession.bytes(for:)` streams it as lines.
private final class MockAIProtocol: URLProtocol, @unchecked Sendable {
    struct Fixture {
        let statusCode: Int
        let body: String
    }

    nonisolated(unsafe) static var fixture: Fixture?
    nonisolated(unsafe) private static var lock = NSLock()

    override class func canInit(with request: URLRequest) -> Bool {
        request.url?.host == "mock.test"
    }

    override class func canonicalRequest(for request: URLRequest) -> URLRequest {
        request
    }

    override func startLoading() {
        let fixture = Self.fixture
        let data = fixture?.body.data(using: .utf8) ?? Data()
        let response = HTTPURLResponse(
            url: request.url!,
            statusCode: fixture?.statusCode ?? 200,
            httpVersion: "HTTP/1.1",
            headerFields: [
                "Content-Type": "application/x-ndjson",
                "Content-Length": "\(data.count)",
            ]
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

/// Thread-safe event collector for the streaming assertions.
private final class LockedEvents: @unchecked Sendable {
    private let lock = NSLock()
    private var storage: [MobileAIEvent] = []

    func append(_ event: MobileAIEvent) {
        lock.lock()
        storage.append(event)
        lock.unlock()
    }

    func snapshot() -> [MobileAIEvent] {
        lock.lock()
        defer { lock.unlock() }
        return storage
    }
}
