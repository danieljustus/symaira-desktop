import XCTest

@testable import SymDictationCore

final class HotkeyEventMapperTests: XCTestCase {
  private let fnFlag: UInt64 = 0x0080_0000
  private let rightOptionFlag: UInt64 = 0x0000_0040

  func testFnPressAndReleaseMapToKeyDownAndKeyUp() {
    var mapper = HotkeyEventMapper(hotkey: .fn)
    XCTAssertEqual(
      mapper.map(.flagsChanged(keyCode: 63, flags: fnFlag, time: 1.0), dictationActive: false),
      HotkeyMapping(events: [.keyDown(at: 1.0)]))
    XCTAssertEqual(
      mapper.map(.flagsChanged(keyCode: 63, flags: 0, time: 2.5), dictationActive: true),
      HotkeyMapping(events: [.keyUp(at: 2.5)]))
  }

  func testRepeatedFlagsChangedWithoutEdgeIsIgnored() {
    var mapper = HotkeyEventMapper(hotkey: .fn)
    _ = mapper.map(.flagsChanged(keyCode: 63, flags: fnFlag, time: 0), dictationActive: false)
    XCTAssertEqual(
      mapper.map(.flagsChanged(keyCode: 63, flags: fnFlag, time: 0.1), dictationActive: true),
      .ignored)
  }

  func testOtherModifierKeysAreIgnored() {
    var mapper = HotkeyEventMapper(hotkey: .fn)
    // Left Shift (56) must not be read as Fn even though the flags word is non-zero.
    XCTAssertEqual(
      mapper.map(.flagsChanged(keyCode: 56, flags: 0x20102, time: 0), dictationActive: false),
      .ignored)
  }

  func testRightOptionUsesDeviceDependentBitSoLeftOptionIsNotConfused() {
    var mapper = HotkeyEventMapper(hotkey: .rightOption)
    // Left Option (58) pressed: ignored.
    XCTAssertEqual(
      mapper.map(.flagsChanged(keyCode: 58, flags: 0x80020, time: 0), dictationActive: false),
      .ignored)
    // Right Option (61) pressed while left Option is held: keyDown.
    XCTAssertEqual(
      mapper.map(
        .flagsChanged(keyCode: 61, flags: 0x80000 | rightOptionFlag | 0x20, time: 1),
        dictationActive: false),
      HotkeyMapping(events: [.keyDown(at: 1)]))
    // Right Option released while left Option stays held: generic flag still set, device bit gone.
    XCTAssertEqual(
      mapper.map(.flagsChanged(keyCode: 61, flags: 0x80000 | 0x20, time: 2), dictationActive: true),
      HotkeyMapping(events: [.keyUp(at: 2)]))
  }

  func testEscapeIsSwallowedOnlyWhileDictationIsActive() {
    var mapper = HotkeyEventMapper()
    XCTAssertEqual(
      mapper.map(.keyDown(keyCode: 53, isRepeat: false, time: 0), dictationActive: false),
      .ignored)
    XCTAssertEqual(
      mapper.map(.keyUp(keyCode: 53, time: 0.1), dictationActive: false), .ignored)

    XCTAssertEqual(
      mapper.map(.keyDown(keyCode: 53, isRepeat: false, time: 1), dictationActive: true),
      HotkeyMapping(events: [.escape], swallow: true))
    // Auto-repeat of the held Esc neither cancels again nor leaks to the app.
    XCTAssertEqual(
      mapper.map(.keyDown(keyCode: 53, isRepeat: true, time: 1.1), dictationActive: true),
      HotkeyMapping(events: [], swallow: true))
    // The matching key-up is swallowed too, even if dictation already ended.
    XCTAssertEqual(
      mapper.map(.keyUp(keyCode: 53, time: 1.2), dictationActive: false),
      HotkeyMapping(events: [], swallow: true))
    // A later unrelated Esc key-up passes through.
    XCTAssertEqual(mapper.map(.keyUp(keyCode: 53, time: 2), dictationActive: false), .ignored)
  }

  func testOtherKeyWhileHotkeyHeldCancelsAsChord() {
    var mapper = HotkeyEventMapper(hotkey: .fn)
    _ = mapper.map(.flagsChanged(keyCode: 63, flags: fnFlag, time: 0), dictationActive: false)
    XCTAssertEqual(
      mapper.map(.keyDown(keyCode: 51, isRepeat: false, time: 0.1), dictationActive: true),
      HotkeyMapping(events: [.escape]))
    // Not our key and not swallowed.
    XCTAssertFalse(
      mapper.map(.keyDown(keyCode: 51, isRepeat: false, time: 0.1), dictationActive: true).swallow)
  }

  func testTypingInHandsFreeModeIsNotAChord() {
    var mapper = HotkeyEventMapper(hotkey: .fn)
    // Hotkey is up (hands-free recording): other keys do nothing.
    XCTAssertEqual(
      mapper.map(.keyDown(keyCode: 0, isRepeat: false, time: 0), dictationActive: true), .ignored)
  }

  func testKeyDownWithoutActiveDictationNeverCancels() {
    var mapper = HotkeyEventMapper(hotkey: .fn)
    _ = mapper.map(.flagsChanged(keyCode: 63, flags: fnFlag, time: 0), dictationActive: false)
    XCTAssertEqual(
      mapper.map(.keyDown(keyCode: 0, isRepeat: false, time: 0.1), dictationActive: false),
      .ignored)
  }

  func testResetForgetsHeldKey() {
    var mapper = HotkeyEventMapper(hotkey: .fn)
    _ = mapper.map(.flagsChanged(keyCode: 63, flags: fnFlag, time: 0), dictationActive: false)
    mapper.reset()
    // After reset the next press edge is a fresh keyDown.
    XCTAssertEqual(
      mapper.map(.flagsChanged(keyCode: 63, flags: fnFlag, time: 5), dictationActive: false),
      HotkeyMapping(events: [.keyDown(at: 5)]))
  }

  func testMapperDrivesGestureMachineEndToEnd() {
    var mapper = HotkeyEventMapper(hotkey: .fn)
    var machine = DictationGestureMachine()
    func feed(_ raw: RawKeyEvent, active: Bool) -> [DictationGestureEffect] {
      mapper.map(raw, dictationActive: active).events.map { machine.handle($0) }
    }
    XCTAssertEqual(
      feed(.flagsChanged(keyCode: 63, flags: fnFlag, time: 0), active: false), [.startRecording])
    XCTAssertEqual(
      feed(.flagsChanged(keyCode: 63, flags: 0, time: 1.5), active: true),
      [.stopRecordingAndProcess])
    XCTAssertEqual(machine.state, .processing)
  }
}
