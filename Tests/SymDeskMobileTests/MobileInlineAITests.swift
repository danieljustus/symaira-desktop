import Foundation
import XCTest
@testable import SymDeskMobile

/// Tests for inline-AI actions (#332): desktop-compatible intent values,
/// selection-aware text surgery, provider fallback, and the accept/undo
/// write discipline (nothing is written before an explicit accept).
@MainActor
final class MobileInlineAITests: XCTestCase {

    // MARK: - Intent contract (desktop `group: inline-ai` parity)

    func testIntentDesktopValuesMatchDesktopContract() {
        XCTAssertEqual(MobileInlineAIIntent.summarize.desktopValue, "summarize")
        XCTAssertEqual(MobileInlineAIIntent.rewrite.desktopValue, "rewrite")
        XCTAssertEqual(MobileInlineAIIntent.continue.desktopValue, "continue")
    }

    func testOnDeviceInstructionFallsBackToRewriteForUnknownIntents() {
        XCTAssertEqual(
            MobileOnDeviceTransformInstruction.forIntent("unknown"),
            MobileOnDeviceTransformInstruction.forIntent("rewrite")
        )
        XCTAssertNotEqual(
            MobileOnDeviceTransformInstruction.forIntent("summarize"),
            MobileOnDeviceTransformInstruction.forIntent("rewrite")
        )
    }

    // MARK: - Selection-aware text surgery

    func testApplyReplacesSelectionOnly() {
        let merged = MobileInlineAIText.apply(
            suggestion: "KURZ",
            to: "Das ist ein sehr langer Absatz mit viel Inhalt.",
            replacing: NSRange(location: 8, length: 22)
        )
        XCTAssertEqual(merged, "Das ist KURZ mit viel Inhalt.")
    }

    func testApplyWholeTextWithoutSelection() {
        let merged = MobileInlineAIText.apply(
            suggestion: "Neuer Text",
            to: "Alter Text",
            replacing: nil
        )
        XCTAssertEqual(merged, "Neuer Text")
    }

    func testApplyWholeTextForInvalidSelection() {
        let merged = MobileInlineAIText.apply(
            suggestion: "Neuer Text",
            to: "Alter Text",
            replacing: NSRange(location: 100, length: 5)
        )
        XCTAssertEqual(merged, "Neuer Text", "out-of-range selection must fall back to whole text")
    }

    // MARK: - Runner: provider selection and fallback

    @MainActor
    func testRunnerUsesServerWhenAvailable() async throws {
        let server = FakeProvider(displayName: "Server", isOnDevice: false, answer: "Server-Antwort")
        let runner = MobileInlineAIRunner(
            primary: { server },
            onDeviceFallback: { FakeProvider(displayName: "On-device", isOnDevice: true, answer: "lokal") }
        )
        let result = try await runner.run(intent: .summarize, text: "Text") { _ in }
        XCTAssertEqual(result.text, "Server-Antwort")
        XCTAssertEqual(result.providerName, "Server")
    }

    @MainActor
    func testRunnerFallsBackToOnDeviceOnServerFailure() async throws {
        let server = FakeProvider(displayName: "Server", isOnDevice: false, error: TestError.boom)
        let device = FakeProvider(displayName: "On-device", isOnDevice: true, answer: "lokale Antwort")
        let runner = MobileInlineAIRunner(
            primary: { server },
            onDeviceFallback: { device }
        )
        let result = try await runner.run(intent: .rewrite, text: "Text") { _ in }
        XCTAssertEqual(result.text, "lokale Antwort")
        XCTAssertEqual(result.providerName, "On-device")
        let serverCalled = await server.transformCalled()
        let deviceCalled = await device.transformCalled()
        XCTAssertTrue(serverCalled)
        XCTAssertTrue(deviceCalled)
    }

    @MainActor
    func testRunnerThrowsWithoutAnyProvider() async {
        let runner = MobileInlineAIRunner(primary: { nil }, onDeviceFallback: { nil })
        do {
            _ = try await runner.run(intent: .summarize, text: "Text") { _ in }
            XCTFail("expected throw")
        } catch {
            XCTAssertTrue(error is MobileAIClient.AIError)
        }
    }

    @MainActor
    func testRunnerStreamsAnswerEventsInOrder() async throws {
        let server = FakeProvider(displayName: "Server", isOnDevice: false, chunks: ["Erster ", "zweiter ", "dritter"])
        let runner = MobileInlineAIRunner(primary: { server }, onDeviceFallback: { nil })
        let received = StringCollector()
        let result = try await runner.run(intent: .continue, text: "Text") { event in
            if event.type == .answer, let text = event.text {
                received.append(text)
            }
        }
        XCTAssertEqual(received.all, ["Erster ", "zweiter ", "dritter"])
        XCTAssertEqual(result.text, "Erster zweiter dritter")
    }

    // MARK: - Model write discipline (accept/undo)

    @MainActor
    func testNoWriteBeforeAccept() async {
        let server = FakeProvider(displayName: "Server", isOnDevice: false, answer: "Zusammenfassung")
        let recorder = SaveRecorder()
        let model = makeModel(server: server, recorder: recorder)

        model.start(intent: .summarize, text: "Langer Text", selectedRange: nil)
        // Wait for the stream to finish.
        await waitUntil { model.phase == .done }

        XCTAssertTrue(model.hasResult)
        let savedBeforeAccept = await recorder.saved
        XCTAssertEqual(savedBeforeAccept, [], "streaming alone must never write")
    }

    @MainActor
    func testAcceptWritesMergedSuggestion() async {
        let server = FakeProvider(displayName: "Server", isOnDevice: false, answer: "Kurzfassung")
        let recorder = SaveRecorder()
        let model = makeModel(server: server, recorder: recorder)

        model.start(intent: .summarize, text: "Langer Text", selectedRange: nil)
        await waitUntil { model.phase == .done }

        await model.accept(currentText: "Langer Text", selectedRange: nil)
        let savedAfterAccept = await recorder.saved
        XCTAssertEqual(savedAfterAccept, ["Kurzfassung"], "whole-text accept saves the suggestion")
        XCTAssertTrue(model.accepted)
        XCTAssertTrue(model.canUndo)
    }

    @MainActor
    func testAcceptWithSelectionMergesIntoEditorText() async {
        let server = FakeProvider(displayName: "Server", isOnDevice: false, answer: "KURZ")
        let recorder = SaveRecorder()
        let model = makeModel(server: server, recorder: recorder)

        let editorText = "Das ist ein sehr langer Absatz."
        model.start(intent: .rewrite, text: "ein sehr langer", selectedRange: NSRange(location: 8, length: 15))
        await waitUntil { model.phase == .done }

        await model.accept(currentText: editorText, selectedRange: NSRange(location: 8, length: 15))
        let saved = await recorder.saved
        XCTAssertEqual(saved, ["Das ist KURZ Absatz."])
    }

    @MainActor
    func testUndoRestoresOriginalThroughSameWritePath() async {
        let server = FakeProvider(displayName: "Server", isOnDevice: false, answer: "Neu")
        let recorder = SaveRecorder()
        let model = makeModel(server: server, recorder: recorder, original: "Original")

        model.start(intent: .summarize, text: "Original", selectedRange: nil)
        await waitUntil { model.phase == .done }
        await model.accept(currentText: "Original", selectedRange: nil)
        let saved = await recorder.saved
        XCTAssertEqual(saved, ["Neu"])

        await model.undo()
        let savedAfterUndo = await recorder.saved
        XCTAssertEqual(savedAfterUndo, ["Neu", "Original"])
        XCTAssertFalse(model.canUndo)
    }

    @MainActor
    func testFailedStreamShowsErrorAndNeverWrites() async {
        let server = FakeProvider(displayName: "Server", isOnDevice: false, error: TestError.boom)
        let recorder = SaveRecorder()
        let model = makeModel(server: server, recorder: recorder)

        model.start(intent: .summarize, text: "Text", selectedRange: nil)
        await waitUntil { model.phase == .failed }

        XCTAssertNotNil(model.errorMessage)
        await model.accept(currentText: "Text", selectedRange: nil)
        let savedAfterFailure = await recorder.saved
        XCTAssertEqual(savedAfterFailure, [], "failed stream must not write")
    }

    @MainActor
    func testOnDeviceFallbackReportedAndWrites() async {
        let server = FakeProvider(displayName: "Server", isOnDevice: false, error: TestError.boom)
        let device = FakeProvider(displayName: "On-device", isOnDevice: true, answer: "lokal")
        let recorder = SaveRecorder()
        let model = MobileInlineAIModel(
            original: "Text",
            runner: MobileInlineAIRunner(
                primary: { server },
                onDeviceFallback: { device }
            ),
            save: { text in await recorder.save(text) }
        )

        model.start(intent: .rewrite, text: "Text", selectedRange: nil)
        await waitUntil { model.phase == .done }

        XCTAssertEqual(model.activeProviderName, "On-device")
        XCTAssertTrue(model.isOnDevice)
        XCTAssertNotNil(model.capabilityNote)
        await model.accept(currentText: "Text", selectedRange: nil)
        let saved = await recorder.saved
        XCTAssertEqual(saved, ["lokal"])
    }

    // MARK: - On-device transform pipeline (fake model backend)

    func testOnDeviceTransformUsesIntentInstruction() async throws {
        let notes: [MobileNote] = []
        let model = RecordingModel(isAvailable: true)
        let provider = MobileOnDeviceAIProvider(vaultNotes: notes, model: model)

        let collector = StringCollector()
        try await provider.transform(text: "Der alte Text.", intent: "summarize") { event in
            if event.type == .answer, let text = event.text {
                collector.append(text)
            }
        }
        XCTAssertEqual(collector.all, ["Kurz."])
        let prompt = await model.lastPrompt ?? ""
        XCTAssertTrue(prompt.contains("Summarise the text below"))
        XCTAssertTrue(prompt.contains("Der alte Text."))
        XCTAssertTrue(prompt.contains("output only the transformed text"))
    }

    // MARK: - Helpers

    @MainActor
    private func makeModel(
        server: FakeProvider,
        recorder: SaveRecorder,
        original: String = "Langer Text"
    ) -> MobileInlineAIModel {
        MobileInlineAIModel(
            original: original,
            runner: MobileInlineAIRunner(
                primary: { server },
                onDeviceFallback: { nil }
            ),
            save: { text in await recorder.save(text) }
        )
    }

    private func waitUntil(
        timeout: TimeInterval = 3,
        _ condition: @escaping @MainActor () -> Bool
    ) async {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if await MainActor.run(body: condition) { return }
            try? await Task.sleep(nanoseconds: 20_000_000)
        }
        XCTFail("condition not met within \(timeout)s")
    }
}

// MARK: - Fakes

private enum TestError: Error {
    case boom
}

/// Fake provider with either a fixed answer, streamed chunks, or a
/// thrown error — records whether transform was invoked.
private final class FakeProvider: MobileAIProvider, @unchecked Sendable {
    let displayName: String
    let isOnDevice: Bool
    private let answer: String?
    private let chunks: [String]
    private let error: Error?
    private let callFlag = CallFlag()

    init(displayName: String, isOnDevice: Bool, answer: String) {
        self.displayName = displayName
        self.isOnDevice = isOnDevice
        self.answer = answer
        self.chunks = []
        self.error = nil
    }

    init(displayName: String, isOnDevice: Bool, chunks: [String]) {
        self.displayName = displayName
        self.isOnDevice = isOnDevice
        self.answer = nil
        self.chunks = chunks
        self.error = nil
    }

    init(displayName: String, isOnDevice: Bool, error: Error) {
        self.displayName = displayName
        self.isOnDevice = isOnDevice
        self.answer = nil
        self.chunks = []
        self.error = error
    }

    func transformCalled() async -> Bool { await callFlag.isSet }

    func ask(query: String, onEvent: @escaping @Sendable (MobileAIEvent) -> Void) async throws {
        try await transform(text: query, intent: "ask", onEvent: onEvent)
    }

    func transform(text: String, intent: String, onEvent: @escaping @Sendable (MobileAIEvent) -> Void) async throws {
        await callFlag.set()
        if let error {
            throw error
        }
        if let answer {
            onEvent(MobileAIEvent(type: .answer, text: answer, path: nil, title: nil, snippet: nil, score: nil, toolName: nil, status: nil))
        }
        for chunk in chunks {
            onEvent(MobileAIEvent(type: .answer, text: chunk, path: nil, title: nil, snippet: nil, score: nil, toolName: nil, status: nil))
        }
        onEvent(MobileAIEvent(type: .done, text: nil, path: nil, title: nil, snippet: nil, score: nil, toolName: nil, status: nil))
    }
}

/// Actor flag for the fake provider's invocation record.
private actor CallFlag {
    private var value = false
    func set() { value = true }
    var isSet: Bool { value }
}

/// Records every write through the save path.
private actor SaveRecorder {
    private var _saved: [String] = []

    var saved: [String] { _saved }

    func save(_ text: String) {
        _saved.append(text)
    }
}

/// Fake on-device model backend that records the prompt. Actor-bound so
/// the async protocol requirements are isolation-safe.
private actor RecordingModel: MobileOnDeviceModelProtocol {
    let isAvailable: Bool
    let capabilityNote = "On-device model — answers are shorter and weaker than the server model."
    private var _lastPrompt: String?

    init(isAvailable: Bool) {
        self.isAvailable = isAvailable
    }

    var lastPrompt: String? { _lastPrompt }

    func respond(to prompt: String) async throws -> String {
        _lastPrompt = prompt
        return "Kurz."
    }
}

/// Lock-protected string collector for synchronous @Sendable callbacks.
private final class StringCollector: @unchecked Sendable {
    private let lock = NSLock()
    private var values: [String] = []

    func append(_ value: String) {
        lock.lock()
        values.append(value)
        lock.unlock()
    }

    var all: [String] {
        lock.lock()
        defer { lock.unlock() }
        return values
    }
}
