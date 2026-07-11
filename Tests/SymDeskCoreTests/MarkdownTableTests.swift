import XCTest
@testable import SymDeskCore

final class MarkdownTableTests: XCTestCase {

    func testParseAndSerializeRoundTrip() {
        let lines = ["| A | B |", "| --- | --- |", "| 1 | 2 |"]
        let table = MarkdownTable.parse(lines: lines)
        XCTAssertEqual(table?.header, ["A", "B"])
        XCTAssertEqual(table?.rows, [["1", "2"]])
        let out = table!.serialize()
        XCTAssertTrue(out.contains("| A"))
        XCTAssertTrue(out.contains("| ---"))
        // Serialized output must reparse identically
        let reparsed = MarkdownTable.parse(lines: out.components(separatedBy: "\n"))
        XCTAssertEqual(reparsed?.header, table?.header)
        XCTAssertEqual(reparsed?.rows, table?.rows)
    }

    func testSerializePadsRaggedRows() {
        let table = MarkdownTable(header: ["A", "B", "C"], alignments: [], rows: [["1"]])
        let out = table.serialize()
        for line in out.components(separatedBy: "\n") {
            XCTAssertEqual(line.filter { $0 == "|" }.count, 4, "every row has all columns: \(line)")
        }
    }

    func testSerializePreservesAlignment() {
        let table = MarkdownTable(header: ["A", "B"], alignments: [":--", ":-:"], rows: [])
        let sep = table.serialize().components(separatedBy: "\n")[1]
        XCTAssertTrue(sep.contains(":-"))
        XCTAssertTrue(sep.contains("-:"))
    }
}

final class MarkdownTableEditorTests: XCTestCase {

    let doc = """
    # Title

    | A | B |
    | --- | --- |
    | 1 | 2 |

    tail
    """

    private func cursorInside(_ needle: String) -> Int {
        let ns = doc as NSString
        let r = ns.range(of: needle)
        return r.location + r.length
    }

    func testIsInTable() {
        XCTAssertTrue(MarkdownTableEditor.isInTable(doc, cursor: cursorInside("| 1")))
        XCTAssertFalse(MarkdownTableEditor.isInTable(doc, cursor: 0))
        XCTAssertFalse(MarkdownTableEditor.isInTable(doc, cursor: (doc as NSString).length))
    }

    func testNextCellMovesWithinRow() {
        let edit = MarkdownTableEditor.nextCell(in: doc, cursor: cursorInside("| 1"))
        XCTAssertNotNil(edit)
        // Cursor should now sit at end of cell "2"
        let ns = edit!.text as NSString
        let upToCursor = ns.substring(to: edit!.cursor)
        XCTAssertTrue(upToCursor.hasSuffix("2"), "cursor after '2', got: ...\(upToCursor.suffix(10))")
    }

    func testNextCellAtEndAppendsRow() {
        let edit = MarkdownTableEditor.nextCell(in: doc, cursor: cursorInside("| 1 | 2"))
        XCTAssertNotNil(edit)
        let tableLines = edit!.text.components(separatedBy: "\n").filter { MarkdownTable.isTableLine($0) }
        XCTAssertEqual(tableLines.count, 4, "new empty row appended")
        // Still a valid table
        XCTAssertNotNil(MarkdownTable.parse(lines: tableLines))
    }

    func testPreviousCellFromFirstHeaderCellReturnsNil() {
        let edit = MarkdownTableEditor.previousCell(in: doc, cursor: cursorInside("| A"))
        XCTAssertNil(edit)
    }

    func testPreviousCellWrapsToHeader() {
        let edit = MarkdownTableEditor.previousCell(in: doc, cursor: cursorInside("| 1"))
        XCTAssertNotNil(edit)
        let ns = edit!.text as NSString
        XCTAssertTrue(ns.substring(to: edit!.cursor).hasSuffix("B"))
    }

    func testInsertRowBelow() {
        let edit = MarkdownTableEditor.insertRowBelow(in: doc, cursor: cursorInside("| 1"))
        let tableLines = edit!.text.components(separatedBy: "\n").filter { MarkdownTable.isTableLine($0) }
        XCTAssertEqual(tableLines.count, 4)
        let parsed = MarkdownTable.parse(lines: tableLines)
        XCTAssertEqual(parsed?.rows.count, 2)
        XCTAssertEqual(parsed?.rows[1], ["", ""])
    }

    func testAddAndRemoveColumn() {
        let added = MarkdownTableEditor.addColumn(in: doc, cursor: cursorInside("| 1"))!
        let addedTable = MarkdownTable.parse(lines: added.text.components(separatedBy: "\n").filter { MarkdownTable.isTableLine($0) })
        XCTAssertEqual(addedTable?.columnCount, 3)

        let removed = MarkdownTableEditor.removeColumn(in: doc, cursor: cursorInside("| 1"))!
        let removedTable = MarkdownTable.parse(lines: removed.text.components(separatedBy: "\n").filter { MarkdownTable.isTableLine($0) })
        XCTAssertEqual(removedTable?.columnCount, 1)
        XCTAssertEqual(removedTable?.header, ["B"])
        XCTAssertEqual(removedTable?.rows, [["2"]])
    }

    func testRemoveLastColumnRefused() {
        let one = "| A |\n| --- |\n| 1 |"
        XCTAssertNil(MarkdownTableEditor.removeColumn(in: one, cursor: 2))
    }

    func testTabNeverProducesInvalidMarkdown() {
        var text = doc
        var cursor = cursorInside("| A")
        for _ in 0..<12 {
            guard let edit = MarkdownTableEditor.nextCell(in: text, cursor: cursor) else {
                XCTFail("expected to stay in table")
                return
            }
            text = edit.text
            cursor = edit.cursor
            let tableLines = text.components(separatedBy: "\n").filter { MarkdownTable.isTableLine($0) }
            let parsed = MarkdownTable.parse(lines: tableLines)
            XCTAssertNotNil(parsed)
            for line in tableLines {
                XCTAssertTrue(line.hasPrefix("|") && line.hasSuffix("|"), "valid pipe row: \(line)")
            }
        }
        // Surrounding document is untouched
        XCTAssertTrue(text.hasPrefix("# Title"))
        XCTAssertTrue(text.hasSuffix("tail"))
    }

    func testDocumentOutsideTableUnchanged() {
        let edit = MarkdownTableEditor.insertRowBelow(in: doc, cursor: cursorInside("| A"))!
        XCTAssertTrue(edit.text.contains("# Title"))
        XCTAssertTrue(edit.text.contains("tail"))
    }
}

final class VaultAssetsTests: XCTestCase {

    func testCollisionSafeName() {
        var taken: Set<String> = ["shot.png", "shot-2.png"]
        let name = VaultAssets.collisionSafeName(base: "shot", ext: "png") { taken.contains($0) }
        XCTAssertEqual(name, "shot-3.png")
        taken.removeAll()
        XCTAssertEqual(VaultAssets.collisionSafeName(base: "shot", ext: "png") { taken.contains($0) }, "shot.png")
    }

    func testSanitizeStripsSeparators() {
        XCTAssertEqual(VaultAssets.sanitize("a/b\\c:d"), "a-b-c-d")
        XCTAssertEqual(VaultAssets.sanitize(""), "pasted-image")
    }

    func testStoreWritesFileAndReturnsRelativePath() throws {
        let tmp = NSTemporaryDirectory() + "vault-assets-test-\(UUID().uuidString)"
        try FileManager.default.createDirectory(atPath: tmp, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(atPath: tmp) }

        let data = Data([0x89, 0x50, 0x4E, 0x47])
        let rel = try VaultAssets.store(imageData: data, fileExtension: "png", vaultRoot: tmp)
        XCTAssertTrue(rel.hasPrefix("assets/"))
        XCTAssertTrue(FileManager.default.fileExists(atPath: tmp + "/" + rel))

        // Second store at the same second must not collide
        let rel2 = try VaultAssets.store(imageData: data, fileExtension: "png", vaultRoot: tmp, now: Date(timeIntervalSince1970: 0))
        let rel3 = try VaultAssets.store(imageData: data, fileExtension: "png", vaultRoot: tmp, now: Date(timeIntervalSince1970: 0))
        XCTAssertNotEqual(rel2, rel3)
    }

    func testMarkdownLinkEncodesSpaces() {
        XCTAssertEqual(VaultAssets.markdownLink(for: "assets/my shot.png"), "![my shot.png](assets/my%20shot.png)")
    }

    func testFolderNameRejectsTraversal() {
        let defaults = UserDefaults(suiteName: "vault-assets-tests-\(UUID().uuidString)")!
        defaults.set("../outside", forKey: VaultAssets.folderDefaultsKey)
        XCTAssertEqual(VaultAssets.folderName(defaults: defaults), "assets")
        defaults.set("media/img", forKey: VaultAssets.folderDefaultsKey)
        XCTAssertEqual(VaultAssets.folderName(defaults: defaults), "media/img")
    }
}
