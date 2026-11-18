import XCTest
@testable import SymDeskCore

/// Decodes the output of `symdesk meeting available|import|refresh|segments|speakers --json`
/// into the types `DeskCore` uses. The files are the very bytes the Rust binary prints:
/// `crates/symdesk-cli/tests/meeting_import.rs`
/// (`json_output_matches_the_files_the_swift_decoder_test_reads`) asserts that the
/// binary still produces them byte for byte, so both sides are pinned to one file.
final class MeetingImportJSONTests: XCTestCase {
    private func sample(_ name: String, file: StaticString = #filePath) throws -> Data {
        // Tests/SymDeskCoreTests/<this file> -> repository root.
        let root = URL(fileURLWithPath: "\(file)")
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let url = root.appendingPathComponent("crates/symdesk-cli/tests/fixtures/meeting-json/\(name).json")
        return try Data(contentsOf: url)
    }

    private func decode<T: Decodable & Sendable>(_ type: T.Type, _ name: String) throws -> T {
        // The same path `DeskCore.runDecoding` takes.
        try DeskCore.decodeTolerantOfNullArray(type, from: try sample(name))
    }

    private let kitchenSink = "11111111-1111-1111-1111-111111111111"

    func testAvailableDecodesIntoAvailableMeeting() throws {
        let list = try decode([AvailableMeeting].self, "available")
        XCTAssertEqual(list.map(\.meetingID), ["00000000-0000-0000-0000-000000000001", kitchenSink])
        XCTAssertEqual(list.map(\.source), ["imported", "imported"])
    }

    func testImportDecodesTheNotePath() throws {
        struct ImportResult: Codable, Sendable { let path: String }
        let result = try decode(ImportResult.self, "import")
        XCTAssertEqual(result.path, "meetings/meeting-\(kitchenSink).md")
    }

    func testRefreshPreviewAndApplyDecodeIntoMeetingRefreshOutcome() throws {
        let preview = try decode(MeetingRefreshOutcome.self, "refresh")
        XCTAssertEqual(preview.path, "meetings/meeting-\(kitchenSink).md")
        XCTAssertTrue(preview.changed)
        XCTAssertFalse(preview.applied)
        let lines = try XCTUnwrap(preview.diffLines)
        XCTAssertTrue(lines.contains("- Hello, world! Café — naïve résumé."))
        XCTAssertTrue(lines.contains("+ Hello, there! Café — naïve résumé."))

        let applied = try decode(MeetingRefreshOutcome.self, "refresh-applied")
        XCTAssertTrue(applied.changed)
        XCTAssertTrue(applied.applied)
    }

    func testSegmentsDecodeIntoMeetingSegment() throws {
        let segments = try decode([MeetingSegment].self, "segments")
        XCTAssertEqual(segments.map(\.startMS), [0, 2000, 4000, 3_661_000])
        XCTAssertEqual(segments[0].segmentID, "AAAAAAAA-1111-1111-1111-111111111111")
        XCTAssertEqual(segments[0].speakerID, "speaker_1")
        XCTAssertEqual(segments[0].displayText, "Hello, world! Café — naïve résumé.")
        XCTAssertNil(segments[0].editedText)
        XCTAssertEqual(segments[2].revision, "user_corrected")
        XCTAssertEqual(segments[2].displayText, "Continuing without an identified speaker, edited for clarity.")
    }

    func testSpeakersDecodeIntoMeetingSpeaker() throws {
        let speakers = try decode([MeetingSpeaker].self, "speakers")
        XCTAssertEqual(speakers, [
            MeetingSpeaker(speakerID: "speaker_1", label: "Alice"),
            MeetingSpeaker(speakerID: "speaker_2", label: "Bob"),
            MeetingSpeaker(speakerID: "speaker_unknown", label: "speaker_unknown"),
        ])
    }
}
