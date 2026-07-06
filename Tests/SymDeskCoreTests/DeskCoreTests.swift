import XCTest
@testable import SymDeskCore

final class DeskCoreTests: XCTestCase {
    func testNoteDecoding() throws {
        let json = """
        {
            "path": "/tmp/note.md",
            "title": "My Note",
            "sha256": "abcdef",
            "modified_at": "2026-07-06T12:00:00Z",
            "indexed_at": "2026-07-06T12:00:01Z"
        }
        """.data(using: .utf8)!
        
        let note = try JSONDecoder().decode(Note.self, from: json)
        XCTAssertEqual(note.path, "/tmp/note.md")
        XCTAssertEqual(note.title, "My Note")
        XCTAssertEqual(note.sha256, "abcdef")
        XCTAssertEqual(note.modifiedAt, "2026-07-06T12:00:00Z")
    }

    func testEventDecoding() throws {
        let json = """
        {
            "event": "file_changed",
            "path": "/tmp/note.md",
            "ts": 123456789
        }
        """.data(using: .utf8)!
        
        let ev = try JSONDecoder().decode(VaultEvent.self, from: json)
        XCTAssertEqual(ev.event, "file_changed")
        XCTAssertEqual(ev.path, "/tmp/note.md")
        XCTAssertEqual(ev.ts, 123456789)
    }
}
