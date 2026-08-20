import Foundation
import XCTest

@testable import SymMeetCore

final class PathTests: XCTestCase {
  func testXDGOverridesProducePortableLocations() {
    let paths = SymMeetPaths(
      environment: [
        "XDG_CONFIG_HOME": "/tmp/config",
        "XDG_CACHE_HOME": "/tmp/cache",
        "XDG_DATA_HOME": "/tmp/data",
      ],
      home: URL(fileURLWithPath: "/Users/example", isDirectory: true)
    )

    XCTAssertEqual(paths.configFile.path, "/tmp/config/symmeet/config.toml")
    XCTAssertEqual(paths.modelsDirectory.path, "/tmp/cache/symmeet/models")
    XCTAssertEqual(paths.workDirectory.path, "/tmp/cache/symmeet/work")
    XCTAssertEqual(paths.dataDirectory.path, "/tmp/data/symmeet")
  }

  func testDefaultPathsFollowMacOSXDGConventions() {
    let home = URL(fileURLWithPath: "/Users/example", isDirectory: true)
    let paths = SymMeetPaths(environment: [:], home: home)

    XCTAssertEqual(paths.configFile.path, "/Users/example/.config/symmeet/config.toml")
    XCTAssertEqual(paths.modelsDirectory.path, "/Users/example/.cache/symmeet/models")
    XCTAssertEqual(paths.workDirectory.path, "/Users/example/.cache/symmeet/work")
    XCTAssertEqual(paths.dataDirectory.path, "/Users/example/.local/share/symmeet")
  }
}
