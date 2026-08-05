import Foundation
import XCTest
@testable import SymDeskMobile

/// Tests for the on-device AI fallback (#331): provider selection
/// (server wins, on-device fallback, graceful unavailability), local
/// retrieval grounding, shared prompt construction and the on-device
/// pipeline with a fake model backend.
final class MobileFallbackTests: XCTestCase {

    // MARK: - Provider selection

    func testServerPreferredWhenConnected() {
        let connection = MobileServerConnection(
            url: URL(string: "https://vault.example")!,
            token: String(repeating: "a", count: 32)
        )
        let selection = MobileAIProviderFactory.select(connection: connection, vaultNotes: [])
        XCTAssertNotNil(selection.provider)
        XCTAssertEqual(selection.provider?.displayName, "Server")
        XCTAssertFalse(selection.provider?.isOnDevice ?? true)
        XCTAssertNil(selection.unavailableReason)
    }

    func testOnDeviceFallbackWithoutServer() {
        let selection = MobileAIProviderFactory.select(connection: nil, vaultNotes: [])
        XCTAssertNil(selection.provider, "no server and no supported device model → no provider")
        XCTAssertNotNil(selection.unavailableReason)
        XCTAssertTrue(selection.unavailableReason!.contains("on-device"))
    }

    func testOnDeviceSelectedWhenModelAvailable() {
        // With a fake available model the factory must pick it.
        let model = FakeModel(isAvailable: true, answer: "lokale Antwort")
        let provider = MobileOnDeviceAIProvider(vaultNotes: [], model: model)
        XCTAssertTrue(provider.isAvailable)
        XCTAssertEqual(provider.displayName, "On-device")
        XCTAssertTrue(provider.isOnDevice)
        XCTAssertTrue(provider.capabilityNote.contains("shorter and weaker"))
    }

    func testUnavailableModelExplainsGracefully() {
        let model = FakeModel(isAvailable: false, answer: "")
        let provider = MobileOnDeviceAIProvider(vaultNotes: [], model: model)
        XCTAssertFalse(provider.isAvailable)
        // ask() throws a clear error instead of failing silently.
        let expectation = expectation(description: "ask throws")
        Task {
            do {
                try await provider.ask(query: "x") { _ in }
            } catch {
                expectation.fulfill()
            }
        }
        wait(for: [expectation], timeout: 2)
    }

    // MARK: - Local retrieval (grounding)

    private func note(
        _ name: String,
        title: String,
        body: String,
        tags: [String] = []
    ) throws -> MobileNote {
        let root = URL(fileURLWithPath: "/tmp/SymDeskMobileVault", isDirectory: true)
        let source = """
        ---
        title: "\(title)"
        tags: [\(tags.map { "\"\($0)\"" }.joined(separator: ", "))]
        ---

        \(body)
        """
        return try MobileVaultParser.parse(
            data: Data(source.utf8),
            fileURL: root.appendingPathComponent("\(name).md"),
            root: root,
            modifiedAt: .now
        )
    }

    func testRetrievalRanksTitleOverBody() throws {
        let notes = [
            try note("a", title: "Rechnung Juli", body: "x"),
            try note("b", title: "Notiz", body: "Rechnung Juli Details"),
            try note("c", title: "Anders", body: "kein Treffer"),
        ]
        let retriever = MobileLocalRetriever(notes: notes)
        let results = retriever.retrieve(query: "Rechnung Juli", limit: 10)
        XCTAssertEqual(results.count, 2)
        XCTAssertEqual(results[0].path, "a.md", "title match must rank above body-only match")
        XCTAssertEqual(results[1].path, "b.md")
    }

    func testRetrievalProducesSnippetAroundMatch() throws {
        let longBody = "Absatz eins. " + String(repeating: "Fuelltext ", count: 20) + "Rechnung Juli. " + String(repeating: "Mehr ", count: 30)
        let notes = [try note("a", title: "T", body: longBody)]
        let results = MobileLocalRetriever(notes: notes).retrieve(query: "Rechnung", limit: 5)
        XCTAssertEqual(results.count, 1)
        XCTAssertTrue(results[0].snippet.contains("Rechnung Juli"))
        XCTAssertTrue(results[0].snippet.hasPrefix("…"))
        XCTAssertTrue(results[0].snippet.hasSuffix("…"))
    }

    func testRetrievalEmptyForNoMatch() throws {
        let notes = [try note("a", title: "T", body: "irgendwas")]
        XCTAssertTrue(MobileLocalRetriever(notes: notes).retrieve(query: "Quatsch", limit: 5).isEmpty)
    }

    // MARK: - Prompt construction (one path, provider limits only)

    func testPromptIncludesQuestionAndExcerpts() throws {
        let context = [
            MobileLocalRetriever.Document(path: "inbox/a.md", title: "A", snippet: "Rechnung über 100 €", score: 3)
        ]
        let prompt = MobileAIPromptBuilder.build(query: "Wie hoch?", context: context, onDevice: false)
        XCTAssertTrue(prompt.contains("Wie hoch?"))
        XCTAssertTrue(prompt.contains("inbox/a.md"))
        XCTAssertTrue(prompt.contains("Rechnung über 100 €"))

        let onDevicePrompt = MobileAIPromptBuilder.build(query: "Wie hoch?", context: context, onDevice: true)
        XCTAssertTrue(onDevicePrompt.contains("small on-device model"))
        XCTAssertTrue(onDevicePrompt.contains("concise"))
    }

    func testPromptHandlesEmptyContext() {
        let prompt = MobileAIPromptBuilder.build(query: "x", context: [], onDevice: false)
        XCTAssertTrue(prompt.contains("(no matching documents)"))
    }

    // MARK: - On-device pipeline (fake model)

    func testOnDeviceAnswerIsGroundedAndCited() async throws {
        let notes = [
            try note("a", title: "Rechnung Juli", body: "Die Rechnung über 100 Euro"),
        ]
        let model = FakeModel(isAvailable: true, answer: "Die Rechnung beträgt 100 Euro.")
        let provider = MobileOnDeviceAIProvider(vaultNotes: notes, model: model)

        let collected = LockedEvents()
        try await provider.ask(query: "Wie hoch ist die Rechnung?") { event in
            collected.append(event)
        }
        let events = collected.snapshot()

        // tool search → tool done → citation → answer → done
        XCTAssertEqual(events.count, 5)
        XCTAssertEqual(events[0].type, .tool)
        XCTAssertEqual(events[2].type, .citation)
        XCTAssertEqual(events[2].path, "a.md")
        XCTAssertEqual(events[3].type, .answer)
        XCTAssertEqual(events[3].text, "Die Rechnung beträgt 100 Euro.")
        XCTAssertEqual(events[4].type, .done)

        // The fake model must have received the grounded prompt.
        XCTAssertTrue(model.lastPrompt?.contains("a.md") ?? false)
        XCTAssertTrue(model.lastPrompt?.contains("Rechnung über 100 Euro") ?? false)
        XCTAssertTrue(model.lastPrompt?.contains("small on-device model") ?? false)
    }
}

/// Fake on-device model backend for tests.
private final class FakeModel: MobileOnDeviceModelProtocol, @unchecked Sendable {
    let isAvailable: Bool
    let capabilityNote = "On-device model — answers are shorter and weaker than the server model."
    private let answer: String
    private let state = FakeModelState()

    init(isAvailable: Bool, answer: String) {
        self.isAvailable = isAvailable
        self.answer = answer
    }

    var lastPrompt: String? {
        state.capturedPrompt
    }

    func respond(to prompt: String) async throws -> String {
        state.setPrompt(prompt)
        guard isAvailable else {
            throw MobileAIClient.AIError.notConfigured
        }
        return answer
    }
}

/// Thread-safe event collector for streaming assertions.
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

/// Lock-protected captured prompt (NSLock is not async-safe).
private final class FakeModelState: @unchecked Sendable {
    private let lock = NSLock()
    private var prompt: String?

    var capturedPrompt: String? {
        lock.lock()
        defer { lock.unlock() }
        return prompt
    }

    func set(_ value: String) {
        lock.lock()
        prompt = value
        lock.unlock()
    }
}

extension FakeModelState {
    func setPrompt(_ value: String) { set(value) }
}
