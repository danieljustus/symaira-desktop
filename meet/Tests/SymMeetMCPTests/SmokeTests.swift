import XCTest

@testable import SymMeetMCP

final class SmokeTests: XCTestCase {
  func testExposesTheProtocolSchemaVersion() {
    XCTAssertEqual(SymMeetMCP.protocolSchemaVersion, 1)
  }
}
