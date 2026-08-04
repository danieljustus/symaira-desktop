import XCTest
@testable import SymDesk

/// Regression tests for the version-history diff pane (issue #307).
final class HistoryDiffTests: XCTestCase {

    func testIdenticalTextsProduceNoDiffLines() {
        let text = "line1\nline2\nline3"
        let diff = HistoryView.makeDiff(old: text, new: text)
        XCTAssertTrue(diff.lines.isEmpty, "identical texts must produce an empty diff")
    }

    func testAddedLinesAreMarked() {
        let diff = HistoryView.makeDiff(old: "a\nb", new: "a\nNEW\nb")
        let added = diff.lines.filter { $0.kind == .added }
        let removed = diff.lines.filter { $0.kind == .removed }
        XCTAssertEqual(added.map(\.text), ["NEW"])
        XCTAssertTrue(removed.isEmpty)
    }

    func testRemovedLinesAreMarked() {
        let diff = HistoryView.makeDiff(old: "a\nGONE\nb", new: "a\nb")
        let removed = diff.lines.filter { $0.kind == .removed }
        XCTAssertEqual(removed.map(\.text), ["GONE"])
        XCTAssertTrue(diff.lines.filter { $0.kind == .added }.isEmpty)
    }

    func testReplacementShowsRemovedAndAdded() {
        let diff = HistoryView.makeDiff(old: "a\nOLD\nb", new: "a\nNEW\nb")
        let removed = diff.lines.filter { $0.kind == .removed }.map(\.text)
        let added = diff.lines.filter { $0.kind == .added }.map(\.text)
        XCTAssertEqual(removed, ["OLD"])
        XCTAssertEqual(added, ["NEW"])
    }

    func testTrailingNewlineDoesNotCreateSpuriousDiff() {
        let diff = HistoryView.makeDiff(old: "a\nb\n", new: "a\nb")
        XCTAssertTrue(diff.lines.isEmpty, "trailing newline difference must be ignored")
    }

    func testEmptyOldTextMarksEverythingAdded() {
        let diff = HistoryView.makeDiff(old: "", new: "x\ny")
        XCTAssertEqual(diff.lines.filter { $0.kind == .added }.count, 2)
        XCTAssertTrue(diff.lines.filter { $0.kind == .removed }.isEmpty)
    }
}
