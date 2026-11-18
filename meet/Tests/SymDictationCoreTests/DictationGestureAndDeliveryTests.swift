import XCTest

@testable import SymDictationCore

final class DictationGestureMachineTests: XCTestCase {
  private typealias Step = (
    event: DictationGestureEvent, effect: DictationGestureEffect, state: DictationGestureState
  )

  private func run(_ steps: [Step], file: StaticString = #filePath, line: UInt = #line) {
    var machine = DictationGestureMachine()
    for (index, step) in steps.enumerated() {
      let effect = machine.handle(step.event)
      XCTAssertEqual(effect, step.effect, "step \(index) effect", file: file, line: line)
      XCTAssertEqual(machine.state, step.state, "step \(index) state", file: file, line: line)
    }
  }

  func testHoldToTalk() {
    run([
      (.keyDown(at: 0), .startRecording, .recording(.pushToTalk)),
      (.keyDown(at: 0.1), .none, .recording(.pushToTalk)),  // auto-repeat
      (.keyUp(at: 2.0), .stopRecordingAndProcess, .processing),
      (.processingFinished, .none, .idle),
    ])
  }

  func testDoubleTapSwitchesToHandsFreeEndingOnNextKeyDown() {
    run([
      (.keyDown(at: 0), .startRecording, .recording(.pushToTalk)),
      (.keyUp(at: 0.1), .cancelRecording, .idle),
      (.keyDown(at: 0.3), .startRecording, .recording(.handsFree)),
      (.keyUp(at: 0.4), .none, .recording(.handsFree)),
      (.keyDown(at: 5.0), .stopRecordingAndProcess, .processing),
      (.keyUp(at: 5.1), .none, .processing),
      (.processingFinished, .none, .idle),
    ])
  }

  func testSecondTapTooLateIsNewPushToTalk() {
    run([
      (.keyDown(at: 0), .startRecording, .recording(.pushToTalk)),
      (.keyUp(at: 0.1), .cancelRecording, .idle),
      (.keyDown(at: 0.36), .startRecording, .recording(.pushToTalk)),
      (.keyUp(at: 2), .stopRecordingAndProcess, .processing),
    ])
  }

  func testLongFirstPressDoesNotBecomeHandsFree() {
    run([
      (.keyDown(at: 0), .startRecording, .recording(.pushToTalk)),
      (.keyUp(at: 0.25), .stopRecordingAndProcess, .processing),
      (.processingFinished, .none, .idle),
      (.keyDown(at: 0.3), .startRecording, .recording(.pushToTalk)),
      (.keyUp(at: 1.0), .stopRecordingAndProcess, .processing),
    ])
  }

  func testEscapeCancelsPushToTalkAndIgnoresRelease() {
    run([
      (.keyDown(at: 0), .startRecording, .recording(.pushToTalk)),
      (.escape, .cancelRecording, .cancelled),
      (.keyUp(at: 1), .none, .cancelled),
      (.keyDown(at: 2), .startRecording, .recording(.pushToTalk)),
    ])
  }

  func testEscapeCancelsHandsFree() {
    run([
      (.keyDown(at: 0), .startRecording, .recording(.pushToTalk)),
      (.keyUp(at: 0.1), .cancelRecording, .idle),
      (.keyDown(at: 0.2), .startRecording, .recording(.handsFree)),
      (.escape, .cancelRecording, .cancelled),
    ])
  }

  func testEscapeWhenIdleOrProcessingDoesNothing() {
    run([
      (.escape, .none, .idle),
      (.keyDown(at: 0), .startRecording, .recording(.pushToTalk)),
      (.keyUp(at: 1), .stopRecordingAndProcess, .processing),
      (.escape, .none, .processing),
    ])
  }

  func testKeyDownWhileProcessingIsIgnored() {
    run([
      (.keyDown(at: 0), .startRecording, .recording(.pushToTalk)),
      (.keyUp(at: 1), .stopRecordingAndProcess, .processing),
      (.keyDown(at: 1.1), .none, .processing),
      (.keyUp(at: 1.2), .none, .processing),
      (.processingFinished, .none, .idle),
    ])
  }

  func testEscapeBetweenTapsResetsDoubleTap() {
    run([
      (.keyDown(at: 0), .startRecording, .recording(.pushToTalk)),
      (.keyUp(at: 0.1), .cancelRecording, .idle),
      (.escape, .none, .idle),
      (.keyDown(at: 0.2), .startRecording, .recording(.pushToTalk)),
    ])
  }
}

final class DeliveryLedgerTests: XCTestCase {
  func testDeliversCurrentGenerationExactlyOnce() {
    var ledger = DeliveryLedger()
    let generation = ledger.beginSession(documentID: "doc-1")
    XCTAssertTrue(ledger.shouldDeliver(generation: generation, documentID: "doc-1"))
    XCTAssertFalse(ledger.shouldDeliver(generation: generation, documentID: "doc-1"))
  }

  func testGenerationsIncrease() {
    var ledger = DeliveryLedger()
    let first = ledger.beginSession(documentID: nil)
    let second = ledger.beginSession(documentID: nil)
    XCTAssertGreaterThan(second, first)
    XCTAssertEqual(ledger.currentGeneration, second)
  }

  func testStaleGenerationIsRejected() {
    var ledger = DeliveryLedger()
    let old = ledger.beginSession(documentID: "d")
    let new = ledger.beginSession(documentID: "d")
    XCTAssertFalse(ledger.shouldDeliver(generation: old, documentID: "d"))
    XCTAssertTrue(ledger.shouldDeliver(generation: new, documentID: "d"))
  }

  func testDocumentMismatchRejectedWithoutConsuming() {
    var ledger = DeliveryLedger()
    let generation = ledger.beginSession(documentID: "a")
    XCTAssertFalse(ledger.shouldDeliver(generation: generation, documentID: "b"))
    XCTAssertFalse(ledger.shouldDeliver(generation: generation, documentID: nil))
    XCTAssertTrue(ledger.shouldDeliver(generation: generation, documentID: "a"))
  }

  func testUnknownAndZeroGenerationRejected() {
    var ledger = DeliveryLedger()
    XCTAssertFalse(ledger.shouldDeliver(generation: 0, documentID: nil))
    let generation = ledger.beginSession(documentID: nil)
    XCTAssertFalse(ledger.shouldDeliver(generation: generation + 1, documentID: nil))
  }

  func testInvalidateCurrentBlocksLateResult() {
    var ledger = DeliveryLedger()
    let generation = ledger.beginSession(documentID: "d")
    ledger.invalidateCurrent()
    XCTAssertFalse(ledger.shouldDeliver(generation: generation, documentID: "d"))
  }
}

final class ClipboardRestorePolicyTests: XCTestCase {
  func testTable() {
    let cases:
      [(recorded: Int, current: Int, generation: UInt64, currentGeneration: UInt64, expected: Bool)] =
        [
          (5, 5, 3, 3, true),
          (5, 6, 3, 3, false),  // user copied something newer
          (5, 5, 3, 4, false),  // a newer dictation owns the clipboard now
          (5, 7, 3, 4, false),
        ]
    for item in cases {
      XCTAssertEqual(
        ClipboardRestorePolicy.shouldRestore(
          recordedChangeCount: item.recorded, currentChangeCount: item.current,
          generation: item.generation, currentGeneration: item.currentGeneration),
        item.expected, "\(item)")
    }
  }
}
