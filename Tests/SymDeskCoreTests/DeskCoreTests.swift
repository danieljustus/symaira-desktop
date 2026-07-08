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

    func testDocumentItemDecoding() throws {
        let json = """
        {
            "path": "inbox/receipt-2026.md",
            "title": "Receipt 2026",
            "document_date": "2026-07-01",
            "person": "Daniel",
            "status": "open",
            "due_date": "2026-08-01",
            "confidence": 85,
            "correspondent": "Acme Corp",
            "document_type": "invoice"
        }
        """.data(using: .utf8)!

        let doc = try JSONDecoder().decode(DocumentItem.self, from: json)
        XCTAssertEqual(doc.path, "inbox/receipt-2026.md")
        XCTAssertEqual(doc.title, "Receipt 2026")
        XCTAssertEqual(doc.documentDate, "2026-07-01")
        XCTAssertEqual(doc.person, "Daniel")
        XCTAssertEqual(doc.status, "open")
        XCTAssertEqual(doc.dueDate, "2026-08-01")
        XCTAssertEqual(doc.confidence, 85)
        XCTAssertEqual(doc.correspondent, "Acme Corp")
        XCTAssertEqual(doc.documentType, "invoice")
        XCTAssertEqual(doc.id, doc.path)
    }

    func testDocumentItemDecodingWithEmptyFields() throws {
        let json = """
        {
            "path": "notes/plain.md",
            "title": "Plain Note"
        }
        """.data(using: .utf8)!

        let doc = try JSONDecoder().decode(DocumentItem.self, from: json)
        XCTAssertEqual(doc.path, "notes/plain.md")
        XCTAssertEqual(doc.title, "Plain Note")
        XCTAssertEqual(doc.documentDate, "")
        XCTAssertEqual(doc.status, "")
        XCTAssertEqual(doc.confidence, 0)
    }

    func testDocumentStatusEnum() {
        XCTAssertEqual(DocumentStatus.open.rawValue, "open")
        XCTAssertEqual(DocumentStatus.needsReview.rawValue, "needs_review")
        XCTAssertEqual(DocumentStatus.allCases.count, 6)
    }

    func testDocumentStatusLabelsAndImages() {
        for status in DocumentStatus.allCases {
            XCTAssertFalse(status.label.isEmpty, "\(status.rawValue) should have a label")
            XCTAssertFalse(status.systemImage.isEmpty, "\(status.rawValue) should have a systemImage")
        }
    }

    func testDocFilterPresetDefaults() {
        let presets = DocFilterPreset.defaults
        XCTAssertGreaterThanOrEqual(presets.count, 7)
        XCTAssertEqual(presets[0].status, nil)
        XCTAssertEqual(presets[1].status, .open)
    }

    func testSimilarDocDecoding() throws {
        let json = """
        {
            "path": "other/receipt.md",
            "title": "Other Receipt",
            "similarity": 92
        }
        """.data(using: .utf8)!

        let doc = try JSONDecoder().decode(SimilarDoc.self, from: json)
        XCTAssertEqual(doc.similarity, 92)
        XCTAssertEqual(doc.id, doc.path)
    }

    func testReviewDocDecoding() throws {
        let json = """
        {
            "path": "inbox/scan.md",
            "title": "Scan",
            "confidence": 30,
            "reasons": ["confidence 30 < 70", "missing document_type"]
        }
        """.data(using: .utf8)!

        let doc = try JSONDecoder().decode(ReviewDoc.self, from: json)
        XCTAssertEqual(doc.confidence, 30)
        XCTAssertEqual(doc.reasons.count, 2)
        XCTAssertEqual(doc.reasons[0], "confidence 30 < 70")
    }
}
