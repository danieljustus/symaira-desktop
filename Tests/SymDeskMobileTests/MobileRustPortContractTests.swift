import Foundation
import XCTest
@testable import SymDeskMobile

final class MobileRustPortContractTests: XCTestCase {
    private struct Fixture: Codable, Equatable {
        let schemaVersion: Int
        let filename: String
        let document: String

        enum CodingKeys: String, CodingKey {
            case schemaVersion = "schema_version"
            case filename
            case document
        }
    }

    func testMobileWriterFixtureMatchesCommittedRustPortContract() throws {
        let created = Date(timeIntervalSince1970: 1_752_600_000)
        let fixture = Fixture(
            schemaVersion: 1,
            filename: MobileNoteWriter.filename(for: "Käse / 日本"),
            document: MobileNoteWriter.noteDocument(
                title: "Käse \"Crème\" \\ 日本",
                body: "## Einkauf\n\nMilch und 日本語",
                createdAt: created
            )
        )
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        var encoded = try encoder.encode(fixture)
        encoded.append(0x0A)

        let repositoryRoot = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
        let fixtureURL = repositoryRoot
            .appendingPathComponent("testdata/port/vault/mobile-writer.json")
        let generationRequested = ProcessInfo.processInfo.environment["PORT_GENERATE"] == "1"
        if generationRequested || !FileManager.default.fileExists(atPath: fixtureURL.path) {
            try encoded.write(to: fixtureURL, options: .atomic)
            if !generationRequested {
                XCTFail("generated missing mobile writer fixture; rerun the test to verify it")
            }
        } else {
            XCTAssertEqual(try Data(contentsOf: fixtureURL), encoded)
        }
    }
}
