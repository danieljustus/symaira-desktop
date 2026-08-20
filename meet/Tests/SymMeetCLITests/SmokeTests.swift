import XCTest

@testable import SymMeetCore

final class SmokeTests: XCTestCase {
  func testCLIAndCoreShareTheFirstSchemaVersion() {
    XCTAssertEqual(SymMeetCore.schemaVersion, 1)
  }
}
