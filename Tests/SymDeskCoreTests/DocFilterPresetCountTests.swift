import Foundation
import XCTest
@testable import SymDeskCore

/// The sidebar and the Dashboard resolved a preset's badge by branching on the
/// status alone, so every preset without one — Notes, Documents and Meetings —
/// showed the vault total. A vault of fifteen note-typed documents and no
/// meetings reported "Meetings 15" (issue #440).
final class DocFilterPresetCountTests: XCTestCase {
    private let statusCounts = ["open": 3, "needs_review": 1, "done": 2]
    private let typeCounts = ["note": 15]
    private let total = 15

    private func preset(_ id: String) -> DocFilterPreset {
        guard let match = DocFilterPreset.defaults.first(where: { $0.id == id }) else {
            XCTFail("no preset with id \(id)")
            return DocFilterPreset(id: id, label: id, status: nil)
        }
        return match
    }

    private func count(_ id: String) -> Int {
        preset(id).displayCount(statusCounts: statusCounts, typeCounts: typeCounts, total: total)
    }

    func testTypePresetsReportTheirOwnCount() {
        XCTAssertEqual(count("notes"), 15, "every document in the fixture is note-typed")
        XCTAssertEqual(count("documents"), 0, "no document carries the document type")
        XCTAssertEqual(count("meetings"), 0, "a vault with no meetings must not report any")
    }

    func testStatusPresetsAreUnchanged() {
        XCTAssertEqual(count("open"), 3)
        XCTAssertEqual(count("needs_review"), 1)
        XCTAssertEqual(count("done"), 2)
    }

    func testStatusWithNoMatchingDocumentsReportsZero() {
        XCTAssertEqual(count("paid"), 0, "an absent status is zero, not the vault total")
    }

    func testAllDocumentsReportsTheVaultTotal() {
        XCTAssertEqual(count("all"), 15)
    }

    func testEveryDefaultPresetResolvesByStatusOrTypeOrIsTheTotal() {
        // Guards the branch itself: only the "all" preset may fall through to
        // the vault total, so a new preset cannot silently inherit it.
        for preset in DocFilterPreset.defaults where preset.id != "all" {
            XCTAssertTrue(
                preset.status != nil || preset.fileType != nil,
                "preset \(preset.id) would fall back to the vault total"
            )
        }
    }
}
