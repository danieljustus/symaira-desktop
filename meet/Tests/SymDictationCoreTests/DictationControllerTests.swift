import XCTest

@testable import SymDictationCore

@MainActor
private final class FakeRecorder: DictationRecorder {
  var samples: [Float] = Array(repeating: 0.1, count: 16_000)
  var startError: Error?
  private(set) var starts = 0
  private(set) var finishes = 0
  private(set) var cancels = 0

  func start() throws {
    if let startError { throw startError }
    starts += 1
  }

  func finish() -> [Float] {
    finishes += 1
    return samples
  }

  func cancel() { cancels += 1 }
}

private actor FakeTranscriber: DictationTranscriber {
  private var results: [Result<String, Error>]
  private var gate: CheckedContinuation<Void, Never>?
  private var blocking = false
  private var released = false
  private(set) var calls = 0

  init(_ results: [Result<String, Error>]) { self.results = results }

  func blockNextCall() {
    blocking = true
    released = false
  }

  /// Safe to call before or after the blocked call has started.
  func release() {
    released = true
    gate?.resume()
    gate = nil
  }

  func transcribe(samples: [Float]) async throws -> String {
    calls += 1
    if blocking {
      blocking = false
      if !released { await withCheckedContinuation { gate = $0 } }
    }
    return try results.removeFirst().get()
  }
}

@MainActor
private final class FakePresenter: DictationPresenter {
  enum Call: Equatable {
    case recording(DictationRecordingMode)
    case processing
    case hide
    case card(String, CardReason)
    case notice(DictationNotice)
  }
  private(set) var calls: [Call] = []

  func showRecording(_ mode: DictationRecordingMode) { calls.append(.recording(mode)) }
  func showProcessing() { calls.append(.processing) }
  func hideIndicator() { calls.append(.hide) }
  func showCard(text: String, reason: CardReason) { calls.append(.card(text, reason)) }
  func showNotice(_ notice: DictationNotice) { calls.append(.notice(notice)) }
}

@MainActor
private final class FakeEnvironment: DictationEnvironment {
  var target = InsertionTarget(
    bundleID: "com.apple.TextEdit", focus: .textInput, documentID: "doc")
  var conditions = InsertionConditions()
  var context = DictationContext(appBundleID: "com.apple.TextEdit")

  func currentTarget() -> InsertionTarget { target }
  func currentConditions() -> InsertionConditions { conditions }
  func currentContext() -> DictationContext { context }
}

private struct FakeFailure: Error, LocalizedError {
  var errorDescription: String? { "kaputt" }
}

@MainActor
final class DictationControllerTests: XCTestCase {
  private struct Rig {
    let controller: DictationController
    let recorder: FakeRecorder
    let transcriber: FakeTranscriber
    let presenter: FakePresenter
    let environment: FakeEnvironment
    let pasteboard: FakePasteboard
    let keyboard: FakeKeyboard
  }

  private func makeRig(
    transcripts: [Result<String, Error>] = [.success("hallo welt")],
    vocabulary: DictationVocabulary = .empty
  ) -> Rig {
    let recorder = FakeRecorder()
    let transcriber = FakeTranscriber(transcripts)
    let presenter = FakePresenter()
    let environment = FakeEnvironment()
    let pasteboard = FakePasteboard(text: "original")
    let keyboard = FakeKeyboard()
    let inserter = DictationInserter(
      pasteboard: pasteboard, keyboard: keyboard, restoreDelay: 0, sleep: { _ in })
    let controller = DictationController(
      recorder: recorder,
      transcriber: transcriber,
      pipelineProvider: { DictationPipeline(vocabulary: vocabulary) },
      environment: environment,
      inserter: inserter,
      presenter: presenter,
      indicatorDelay: 0)
    return Rig(
      controller: controller, recorder: recorder, transcriber: transcriber,
      presenter: presenter, environment: environment, pasteboard: pasteboard, keyboard: keyboard)
  }

  private func hold(_ rig: Rig, from start: TimeInterval = 0, for duration: TimeInterval = 1.5) {
    rig.controller.handle(.keyDown(at: start))
    rig.controller.handle(.keyUp(at: start + duration))
  }

  func testPushToTalkTranscribesCorrectsAndPastes() async {
    let vocabulary = DictationVocabulary(corrections: [
      DictationCorrection(wrong: "welt", right: "Welt")
    ])
    let rig = makeRig(vocabulary: vocabulary)

    hold(rig)
    XCTAssertEqual(rig.controller.state, .processing)
    await rig.controller.waitUntilIdle()

    XCTAssertEqual(rig.presenter.calls, [.recording(.pushToTalk), .processing, .hide])
    XCTAssertEqual(rig.keyboard.pastes, 1)
    XCTAssertEqual(rig.pasteboard.writes, ["hallo Welt"])
    XCTAssertEqual(rig.pasteboard.text, "original")
    XCTAssertEqual(rig.controller.state, .idle)
  }

  func testHandsFreeRecordsUntilNextKeyDown() async {
    let rig = makeRig()
    rig.controller.handle(.keyDown(at: 0))
    rig.controller.handle(.keyUp(at: 0.1))  // short tap
    rig.controller.handle(.keyDown(at: 0.3))  // second tap: hands-free
    rig.controller.handle(.keyUp(at: 0.4))
    XCTAssertEqual(rig.controller.state, .recording(.handsFree))
    XCTAssertEqual(rig.recorder.cancels, 1, "the first tap's recording is revoked")
    rig.controller.handle(.keyDown(at: 5))
    await rig.controller.waitUntilIdle()

    XCTAssertEqual(rig.keyboard.pastes, 1)
    XCTAssertTrue(rig.presenter.calls.contains(.recording(.handsFree)))
  }

  func testShortTapStartsNothingAndTranscribesNothing() async {
    let rig = makeRig()
    rig.controller.handle(.keyDown(at: 0))
    rig.controller.handle(.keyUp(at: 0.1))
    await rig.controller.waitUntilIdle()

    XCTAssertEqual(rig.controller.state, .idle)
    XCTAssertEqual(rig.recorder.cancels, 1)
    let calls = await rig.transcriber.calls
    XCTAssertEqual(calls, 0)
    XCTAssertEqual(rig.keyboard.pastes, 0)
  }

  func testEscapeWhileRecordingDiscardsEverything() async {
    let rig = makeRig()
    rig.controller.handle(.keyDown(at: 0))
    rig.controller.handle(.escape)
    rig.controller.handle(.keyUp(at: 2))
    await rig.controller.waitUntilIdle()

    XCTAssertEqual(rig.recorder.cancels, 1)
    XCTAssertEqual(rig.recorder.finishes, 0)
    XCTAssertEqual(rig.keyboard.pastes, 0)
    XCTAssertFalse(rig.controller.isActive)
  }

  func testEscapeWhileProcessingDropsLateResult() async {
    let rig = makeRig()
    await rig.transcriber.blockNextCall()
    hold(rig)
    XCTAssertTrue(rig.controller.isActive)

    rig.controller.handle(.escape)
    XCTAssertEqual(rig.controller.state, .idle)
    await rig.transcriber.release()
    await rig.controller.waitUntilIdle()
    for _ in 0..<20 { await Task.yield() }

    XCTAssertEqual(rig.keyboard.pastes, 0)
    XCTAssertEqual(rig.pasteboard.writes, [])
    XCTAssertFalse(rig.presenter.calls.contains { if case .card = $0 { true } else { false } })
  }

  func testSecondSessionWhileProcessingIsIgnoredNotQueued() async {
    let rig = makeRig()
    await rig.transcriber.blockNextCall()
    hold(rig)
    // Key presses while the first result is still being transcribed do not start a recording.
    rig.controller.handle(.keyDown(at: 3))
    rig.controller.handle(.keyUp(at: 4.5))
    XCTAssertEqual(rig.recorder.starts, 1)
    await rig.transcriber.release()
    await rig.controller.waitUntilIdle()
    XCTAssertEqual(rig.keyboard.pastes, 1)
    XCTAssertEqual(rig.controller.state, .idle)
  }

  func testSecureInputKeepsTextInCardAndNeverUsesClipboard() async {
    let rig = makeRig()
    rig.environment.conditions = InsertionConditions(
      secureInputActive: true, accessibilityTrusted: true)
    hold(rig)
    await rig.controller.waitUntilIdle()

    XCTAssertTrue(rig.presenter.calls.contains(.card("hallo welt", .secureInput)))
    XCTAssertEqual(rig.pasteboard.writes, [])
    XCTAssertEqual(rig.pasteboard.changeCount, 0)
    XCTAssertEqual(rig.keyboard.pastes, 0)
    XCTAssertTrue(rig.keyboard.typed.isEmpty)
  }

  func testMissingAccessibilityShowsCardNotCrash() async {
    let rig = makeRig()
    rig.environment.conditions = InsertionConditions(accessibilityTrusted: false)
    hold(rig)
    await rig.controller.waitUntilIdle()
    XCTAssertTrue(rig.presenter.calls.contains(.card("hallo welt", .accessibilityMissing)))
    XCTAssertEqual(rig.keyboard.pastes, 0)
  }

  func testNoFocusedTextFieldShowsCard() async {
    let rig = makeRig()
    rig.environment.target = InsertionTarget(
      bundleID: "com.apple.finder", focus: .notTextInput, documentID: "doc")
    hold(rig)
    await rig.controller.waitUntilIdle()
    XCTAssertTrue(rig.presenter.calls.contains(.card("hallo welt", .noTextField)))
  }

  func testUserSwitchingAppsWhileProcessingKeepsTextInCard() async {
    let rig = makeRig()
    await rig.transcriber.blockNextCall()
    hold(rig)
    rig.environment.target = InsertionTarget(
      bundleID: "com.apple.Notes", focus: .textInput, documentID: "other")
    await rig.transcriber.release()
    await rig.controller.waitUntilIdle()
    XCTAssertTrue(rig.presenter.calls.contains(.card("hallo welt", .documentChanged)))
    XCTAssertEqual(rig.keyboard.pastes, 0)
  }

  func testChromiumTargetTypesInsteadOfPasting() async {
    let rig = makeRig()
    rig.environment.target = InsertionTarget(
      bundleID: "com.google.Chrome", focus: .unknown, documentID: "doc")
    hold(rig)
    await rig.controller.waitUntilIdle()
    XCTAssertEqual(rig.keyboard.typed, ["hallo welt"])
    XCTAssertEqual(rig.pasteboard.writes, [])
  }

  func testTranscriptionFailureShowsNoticeAndReturnsToIdle() async {
    let rig = makeRig(transcripts: [.failure(FakeFailure())])
    hold(rig)
    await rig.controller.waitUntilIdle()
    XCTAssertTrue(rig.presenter.calls.contains(.notice(.transcriptionFailed("kaputt"))))
    XCTAssertEqual(rig.controller.state, .idle)
    XCTAssertEqual(rig.keyboard.pastes, 0)
  }

  func testEmptyTranscriptInsertsNothing() async {
    let rig = makeRig(transcripts: [.success("  äh  ")])
    hold(rig)
    await rig.controller.waitUntilIdle()
    XCTAssertEqual(rig.keyboard.pastes, 0)
    XCTAssertTrue(rig.presenter.calls.contains(.notice(.nothingHeard)))
  }

  func testMicrophoneFailureShowsNoticeAndNextPressWorks() async {
    let rig = makeRig()
    rig.recorder.startError = FakeFailure()
    rig.controller.handle(.keyDown(at: 0))
    XCTAssertTrue(rig.presenter.calls.contains(.notice(.microphoneUnavailable("kaputt"))))
    XCTAssertFalse(rig.controller.isActive)
    rig.controller.handle(.keyUp(at: 1))

    rig.recorder.startError = nil
    hold(rig, from: 5)
    await rig.controller.waitUntilIdle()
    XCTAssertEqual(rig.keyboard.pastes, 1)
  }

  func testTooShortRecordingIsDroppedBeforeTranscription() async {
    let rig = makeRig()
    rig.recorder.samples = Array(repeating: 0.1, count: 1_000)
    hold(rig)
    await rig.controller.waitUntilIdle()
    let calls = await rig.transcriber.calls
    XCTAssertEqual(calls, 0)
    XCTAssertEqual(rig.controller.state, .idle)
  }

  func testShutdownCancelsRecordingAndHidesIndicator() {
    let rig = makeRig()
    rig.controller.handle(.keyDown(at: 0))
    rig.controller.shutdown()
    XCTAssertEqual(rig.recorder.cancels, 1)
    XCTAssertEqual(rig.presenter.calls.last, .hide)
    XCTAssertEqual(rig.controller.state, .idle)
  }
}
