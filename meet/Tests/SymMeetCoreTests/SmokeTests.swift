import XCTest

@testable import SymMeetCore

final class SmokeTests: XCTestCase {
  func testExposesTheInitialSchemaVersion() {
    XCTAssertEqual(SymMeetCore.schemaVersion, 1)
  }
}
