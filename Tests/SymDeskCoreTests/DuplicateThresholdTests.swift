import Foundation
import XCTest
@testable import SymDeskCore

/// The Possible Duplicates screen asks the reader to merge a group and trash
/// the rest, so a group must never contain unrelated documents. Scanning at 50
/// put every document of a vault that shares a frontmatter and heading
/// skeleton into a single group (issue #439). These pin the threshold the app
/// requests so it cannot drift back down unnoticed.
@MainActor
final class DuplicateThresholdTests: XCTestCase {
    func testDefaultThresholdStaysAboveTheDissimilarityBoundary() {
        // The SimHash tests treat anything below 70% as dissimilar, so the
        // duplicate bar has to sit above that to mean "near-identical".
        XCTAssertGreaterThan(
            DeskCore.defaultDuplicateThreshold,
            70,
            "a duplicate group must not be built from documents the project itself calls dissimilar"
        )
        // Measured band: below 65 unrelated documents join the group, above 85
        // a genuine near-duplicate pair is missed.
        XCTAssertLessThanOrEqual(
            DeskCore.defaultDuplicateThreshold,
            85,
            "raising the bar past 85 stops catching documents that differ by only a word or two"
        )
        XCTAssertEqual(DeskCore.defaultDuplicateThreshold, 85)
    }

    func testScanRequestsTheDefaultThreshold() {
        let args = DeskCore.duplicatesArguments(threshold: DeskCore.defaultDuplicateThreshold, vaultArgs: [])

        XCTAssertEqual(args, ["duplicates", "--threshold", "85", "--json"])
    }

    func testExplicitThresholdIsPassedThrough() {
        let args = DeskCore.duplicatesArguments(threshold: 75, vaultArgs: ["--vault", "/tmp/v"])

        XCTAssertEqual(args, ["duplicates", "--threshold", "75", "--json", "--vault", "/tmp/v"])
    }
}
