import SymMeetCore
import SymMeetWhisperKit
import XCTest

final class WhisperKitAdapterTests: XCTestCase {
  func testCapabilitiesStayWithinCoreContract() {
    let capabilities = WhisperKitEngine.declaredCapabilities
    XCTAssertTrue(capabilities.supportsSegmentTimestamps)
    XCTAssertTrue(capabilities.supportsWordTimestamps)
    XCTAssertFalse(capabilities.supportsDiarization)
    XCTAssertEqual(capabilities.requiredArchitectures, ["arm64"])
  }

  func testModelCatalogUsesWhisperKitEngine() {
    XCTAssertTrue(ModelCatalog.beta.descriptors.allSatisfy { $0.engineID == "whisperkit" })
  }
}
