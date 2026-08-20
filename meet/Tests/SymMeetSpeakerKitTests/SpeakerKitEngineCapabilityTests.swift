import XCTest

@testable import SymMeetCore
@testable import SymMeetSpeakerKit

final class SpeakerKitEngineCapabilityTests: XCTestCase {
  func testCapabilitiesDeclareDiarizationSupport() {
    let capabilities = SpeakerKitEngine.declaredCapabilities
    XCTAssertTrue(capabilities.supportsDiarization)
    XCTAssertFalse(capabilities.supportsStreaming)
    XCTAssertFalse(capabilities.supportsWordTimestamps)
    XCTAssertFalse(capabilities.supportsSegmentTimestamps)
  }

  func testEngineIDIsSpeakerKit() {
    XCTAssertEqual(SpeakerKitEngine.declaredCapabilities.requiredArchitectures, ["arm64"])
  }
}
