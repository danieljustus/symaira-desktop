import XCTest

@testable import SymDictationCore

@MainActor
final class DictationInserterTests: XCTestCase {
  private let ok = InsertionConditions()
  private let editor = InsertionTarget(
    bundleID: "com.apple.TextEdit", focus: .textInput, documentID: "doc")
  private let chrome = InsertionTarget(
    bundleID: "com.google.Chrome", focus: .textInput, documentID: "doc")

  private func makeInserter(
    _ pasteboard: FakePasteboard,
    _ keyboard: FakeKeyboard,
    sleep: @escaping @MainActor (TimeInterval) async -> Void = { _ in }
  ) -> DictationInserter {
    DictationInserter(pasteboard: pasteboard, keyboard: keyboard, restoreDelay: 0.4, sleep: sleep)
  }

  func testPasteWritesTextPostsPasteAndRestoresOriginalClipboard() async {
    let pasteboard = FakePasteboard(text: "meine Notiz")
    let keyboard = FakeKeyboard()
    var textAtPasteTime: String?
    keyboard.onPaste = { textAtPasteTime = pasteboard.text }
    let inserter = makeInserter(pasteboard, keyboard)
    let generation = inserter.beginSession(documentID: "doc")

    let outcome = await inserter.deliver(
      "Hallo Welt", generation: generation, target: editor, conditions: ok)

    XCTAssertEqual(outcome, .inserted(.paste))
    XCTAssertEqual(textAtPasteTime, "Hallo Welt")
    XCTAssertEqual(keyboard.pastes, 1)
    XCTAssertEqual(pasteboard.text, "meine Notiz")
    XCTAssertEqual(pasteboard.restores, 1)
  }

  func testForeignCopyBetweenPasteAndRestoreIsNeverOverwritten() async {
    let pasteboard = FakePasteboard(text: "alt")
    let keyboard = FakeKeyboard()
    let gates = SleepGates()
    let inserter = makeInserter(pasteboard, keyboard, sleep: { await gates.sleep($0) })
    let generation = inserter.beginSession(documentID: "doc")

    let delivery = Task { @MainActor in
      await inserter.deliver("Diktat", generation: generation, target: editor, conditions: ok)
    }
    await gates.waitForSleepers(1)
    pasteboard.foreignCopy("neu kopiert")
    gates.releaseAll()
    let outcome = await delivery.value

    XCTAssertEqual(outcome, .inserted(.paste))
    XCTAssertEqual(pasteboard.text, "neu kopiert")
    XCTAssertEqual(pasteboard.restores, 0)
  }

  func testSecondSessionDuringRestoreWindowKeepsUsersOriginalClipboard() async {
    let pasteboard = FakePasteboard(text: "original")
    let keyboard = FakeKeyboard()
    let gates = SleepGates()
    let inserter = makeInserter(pasteboard, keyboard, sleep: { await gates.sleep($0) })

    let first = inserter.beginSession(documentID: "doc")
    let firstDelivery = Task { @MainActor in
      await inserter.deliver("eins", generation: first, target: editor, conditions: ok)
    }
    await gates.waitForSleepers(1)

    // A second dictation starts and pastes while the first restore is still pending.
    let second = inserter.beginSession(documentID: "doc")
    XCTAssertGreaterThan(second, first)
    let secondDelivery = Task { @MainActor in
      await inserter.deliver("zwei", generation: second, target: editor, conditions: ok)
    }
    await gates.waitForSleepers(2)

    gates.releaseAll()
    let outcomes = [await firstDelivery.value, await secondDelivery.value]

    XCTAssertEqual(outcomes, [.inserted(.paste), .inserted(.paste)])
    XCTAssertEqual(pasteboard.writes, ["eins", "zwei"])
    // Not "eins", not "zwei": the user's own clipboard comes back, exactly once.
    XCTAssertEqual(pasteboard.text, "original")
    XCTAssertEqual(pasteboard.restores, 1)
  }

  func testSupersededSessionWithoutFurtherPasteRestoresWhenAbandoned() async {
    let pasteboard = FakePasteboard(text: "original")
    let keyboard = FakeKeyboard()
    let gates = SleepGates()
    let inserter = makeInserter(pasteboard, keyboard, sleep: { await gates.sleep($0) })

    let first = inserter.beginSession(documentID: "doc")
    let delivery = Task { @MainActor in
      await inserter.deliver("eins", generation: first, target: editor, conditions: ok)
    }
    await gates.waitForSleepers(1)
    // A second session starts recording and is cancelled before pasting anything.
    _ = inserter.beginSession(documentID: "doc")
    inserter.cancelSession()
    inserter.restoreAbandonedPaste()
    XCTAssertEqual(
      pasteboard.text, "eins", "must not yank the text away while the paste is in flight")
    gates.releaseAll()
    _ = await delivery.value

    XCTAssertEqual(pasteboard.text, "original")
  }

  func testCancelledSessionResultIsDiscardedWithoutTouchingAnything() async {
    let pasteboard = FakePasteboard()
    let keyboard = FakeKeyboard()
    let inserter = makeInserter(pasteboard, keyboard)
    let generation = inserter.beginSession(documentID: "doc")
    inserter.cancelSession()

    let outcome = await inserter.deliver(
      "zu spät", generation: generation, target: editor, conditions: ok)

    XCTAssertEqual(outcome, .discarded)
    XCTAssertEqual(pasteboard.writes, [])
    XCTAssertEqual(keyboard.pastes, 0)
    XCTAssertTrue(keyboard.typed.isEmpty)
  }

  func testResultIsDeliveredOnlyOnce() async {
    let inserter = makeInserter(FakePasteboard(), FakeKeyboard())
    let generation = inserter.beginSession(documentID: "doc")
    let first = await inserter.deliver("a", generation: generation, target: editor, conditions: ok)
    let replay = await inserter.deliver("a", generation: generation, target: editor, conditions: ok)
    XCTAssertEqual(first, .inserted(.paste))
    XCTAssertEqual(replay, .discarded)
  }

  func testSwitchingDocumentKeepsTextInCardInsteadOfPastingIntoWrongApp() async {
    let pasteboard = FakePasteboard()
    let keyboard = FakeKeyboard()
    let inserter = makeInserter(pasteboard, keyboard)
    let generation = inserter.beginSession(documentID: "doc")
    let other = InsertionTarget(
      bundleID: "com.apple.Notes", focus: .textInput, documentID: "other")

    let outcome = await inserter.deliver(
      "Text", generation: generation, target: other, conditions: ok)

    XCTAssertEqual(outcome, .card(.documentChanged))
    XCTAssertEqual(keyboard.pastes, 0)
    XCTAssertEqual(pasteboard.writes, [])
  }

  func testChromiumTypesAndNeverTouchesPasteboard() async {
    let pasteboard = FakePasteboard(text: "unberührt")
    let keyboard = FakeKeyboard()
    let inserter = makeInserter(pasteboard, keyboard)
    let generation = inserter.beginSession(documentID: "doc")

    let outcome = await inserter.deliver(
      "Tippen", generation: generation, target: chrome, conditions: ok)

    XCTAssertEqual(outcome, .inserted(.type))
    XCTAssertEqual(keyboard.typed, ["Tippen"])
    XCTAssertEqual(pasteboard.writes, [])
    XCTAssertEqual(pasteboard.changeCount, 0)
  }

  func testSecureInputNeitherInsertsNorTouchesPasteboard() async {
    let pasteboard = FakePasteboard(text: "geheim")
    let keyboard = FakeKeyboard()
    let inserter = makeInserter(pasteboard, keyboard)
    let generation = inserter.beginSession(documentID: "doc")
    let secure = InsertionConditions(secureInputActive: true, accessibilityTrusted: true)

    let outcome = await inserter.deliver(
      "Passwort?", generation: generation, target: editor, conditions: secure)

    XCTAssertEqual(outcome, .card(.secureInput))
    XCTAssertEqual(keyboard.pastes, 0)
    XCTAssertTrue(keyboard.typed.isEmpty)
    XCTAssertEqual(pasteboard.writes, [])
    XCTAssertEqual(pasteboard.changeCount, 0)
    XCTAssertEqual(pasteboard.text, "geheim")
  }

  func testNoFocusedTextFieldAndMissingAccessibilityBecomeCards() async {
    let pasteboard = FakePasteboard()
    let keyboard = FakeKeyboard()
    let inserter = makeInserter(pasteboard, keyboard)

    var generation = inserter.beginSession(documentID: "doc")
    let noField = InsertionTarget(
      bundleID: "com.apple.finder", focus: .notTextInput, documentID: "doc")
    let first = await inserter.deliver("a", generation: generation, target: noField, conditions: ok)
    XCTAssertEqual(first, .card(.noTextField))

    generation = inserter.beginSession(documentID: "doc")
    let untrusted = InsertionConditions(secureInputActive: false, accessibilityTrusted: false)
    let second = await inserter.deliver(
      "b", generation: generation, target: editor, conditions: untrusted)
    XCTAssertEqual(second, .card(.accessibilityMissing))

    XCTAssertEqual(pasteboard.writes, [])
    XCTAssertEqual(keyboard.pastes, 0)
  }

  func testFailedPasteRestoresClipboardAndKeepsTextInCard() async {
    let pasteboard = FakePasteboard(text: "original")
    let keyboard = FakeKeyboard()
    keyboard.failPaste = true
    let inserter = makeInserter(pasteboard, keyboard)
    let generation = inserter.beginSession(documentID: "doc")

    let outcome = await inserter.deliver(
      "Text", generation: generation, target: editor, conditions: ok)

    XCTAssertEqual(outcome, .card(.insertFailed))
    XCTAssertEqual(pasteboard.text, "original")
  }

  func testFailedTypingBecomesCard() async {
    let keyboard = FakeKeyboard()
    keyboard.failType = true
    let inserter = makeInserter(FakePasteboard(), keyboard)
    let generation = inserter.beginSession(documentID: "doc")
    let outcome = await inserter.deliver(
      "Text", generation: generation, target: chrome, conditions: ok)
    XCTAssertEqual(outcome, .card(.insertFailed))
  }
}
