import XCTest
@testable import SymDesk

// Guards the fallback disambiguator added for identically-titled document
// cards (e.g. several "README" notes ingested from different folders):
// the containing folder must be shown so the cards are told apart without
// opening either — see DocumentGridView.swift's DocumentCard.
final class DocumentCardTests: XCTestCase {
    func testNestedPathReturnsContainingFolder() {
        XCTAssertEqual(
            DocumentCard.containingFolder(forPath: "Invoices/2024/receipt.md"),
            "Invoices/2024"
        )
    }

    func testSingleLevelFolderReturnsThatFolder() {
        XCTAssertEqual(
            DocumentCard.containingFolder(forPath: "Personal/README.md"),
            "Personal"
        )
    }

    func testDifferentFoldersProduceDifferentSubtitlesForSameFilename() {
        let first = DocumentCard.containingFolder(forPath: "Legal/README.md")
        let second = DocumentCard.containingFolder(forPath: "Taxes/2023/README.md")

        XCTAssertNotEqual(first, second)
        XCTAssertEqual(first, "Legal")
        XCTAssertEqual(second, "Taxes/2023")
    }

    func testRootLevelFileHasNoContainingFolder() {
        XCTAssertNil(DocumentCard.containingFolder(forPath: "README.md"))
    }

    func testEmptyPathHasNoContainingFolder() {
        XCTAssertNil(DocumentCard.containingFolder(forPath: ""))
    }

    func testLeadingAndTrailingSlashesAreIgnored() {
        XCTAssertEqual(
            DocumentCard.containingFolder(forPath: "/Invoices/2024/receipt.md/"),
            "Invoices/2024"
        )
    }
}
