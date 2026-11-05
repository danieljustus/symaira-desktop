import Foundation
import XCTest
@testable import SymDeskMobile

final class MobileVaultParserTests: XCTestCase {
    func testParsesContractMetadataAndObsidianAttachment() throws {
        let root = URL(fileURLWithPath: "/tmp/SymDeskMobileVault", isDirectory: true)
        let noteURL = root.appendingPathComponent("inbox/invoice.md")
        let source = """
        ---
        title: "July invoice"
        created: "2026-07-01T10:00:00Z"
        tags:
          - finance
          - household
        document_type: invoice
        status: open
        due_date: "2026-07-31"
        confidence: 92
        ---

        # July invoice

        ![[invoice.pdf]]
        """

        let note = try MobileVaultParser.parse(
            data: Data(source.utf8),
            fileURL: noteURL,
            root: root,
            modifiedAt: Date(timeIntervalSince1970: 42)
        )

        XCTAssertEqual(note.path, "inbox/invoice.md")
        XCTAssertEqual(note.title, "July invoice")
        XCTAssertEqual(note.tags, ["finance", "household"])
        XCTAssertEqual(note.documentType, "invoice")
        XCTAssertEqual(note.status, "open")
        XCTAssertEqual(note.confidence, 92)
        XCTAssertEqual(note.attachmentReferences, ["invoice.pdf"])
        XCTAssertTrue(note.isDocument)
        XCTAssertTrue(note.searchText.contains("july invoice"))
    }

    func testResolvesOnlyAttachmentsInsideVault() throws {
        let temporary = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString, isDirectory: true)
        let inbox = temporary.appendingPathComponent("inbox", isDirectory: true)
        try FileManager.default.createDirectory(at: inbox, withIntermediateDirectories: true)
        let attachment = inbox.appendingPathComponent("scan.pdf")
        try Data("pdf".utf8).write(to: attachment)

        let noteURL = inbox.appendingPathComponent("scan.md")
        let note = try MobileVaultParser.parse(
            data: Data("---\ntitle: Scan\n---\n\n![[scan.pdf]]".utf8),
            fileURL: noteURL,
            root: temporary,
            modifiedAt: .now
        )

        XCTAssertEqual(note.attachmentURL(in: temporary), attachment.standardizedFileURL)
    }

    func testNormalizesDiacriticsForSearch() {
        XCTAssertEqual(MobileVaultParser.normalizedSearchQuery("  Überfällig  "), "uberfallig")
    }

    func testDoesNotResolveAttachmentOutsideGrantedVault() throws {
        let root = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        let outside = FileManager.default.temporaryDirectory
            .appendingPathComponent("outside-\(UUID().uuidString).pdf")
        try Data("pdf".utf8).write(to: outside)
        defer { try? FileManager.default.removeItem(at: outside) }

        let source = "---\ntitle: Outside\narchive_path: \(outside.path)\n---\n"
        let note = try MobileVaultParser.parse(
            data: Data(source.utf8),
            fileURL: root.appendingPathComponent("outside.md"),
            root: root,
            modifiedAt: .now
        )

        XCTAssertNil(note.attachmentURL(in: root))
    }

	func testServerURLNormalization() {
		XCTAssertEqual(MobileServerConfig.normalizedURL("http://homeassistant.local:8787/")?.host, "homeassistant.local")
		XCTAssertNil(MobileServerConfig.normalizedURL("homeassistant.local:8787"))
		XCTAssertNil(MobileServerConfig.normalizedURL("https://example.test/api/v1"))
		XCTAssertNil(MobileServerConfig.normalizedURL("https://user:secret@example.test"))
		XCTAssertNil(MobileServerConfig.normalizedURL("https://example.test?token=secret"))
	}

	@MainActor
	func testRecordOpenedTracksMostRecentFirstDedupedAndBounded() throws {
		let store = MobileVaultStore()
		defer { store.resetVault() }

		let root = URL(fileURLWithPath: "/tmp/SymDeskMobileVault", isDirectory: true)
		func note(_ name: String) throws -> MobileNote {
			try MobileVaultParser.parse(
				data: Data("---\ntitle: \(name)\n---\n".utf8),
				fileURL: root.appendingPathComponent("\(name).md"),
				root: root,
				modifiedAt: .now
			)
		}

		let a = try note("a")
		let b = try note("b")
		let c = try note("c")

		store.recordOpened(a)
		store.recordOpened(b)
		store.recordOpened(c)
		XCTAssertEqual(store.recentlyOpenedPaths, ["c.md", "b.md", "a.md"])

		// Re-opening an already-tracked note moves it to the front instead
		// of appearing twice.
		store.recordOpened(a)
		XCTAssertEqual(store.recentlyOpenedPaths, ["a.md", "c.md", "b.md"])

		for i in 0..<12 {
			store.recordOpened(try note("extra\(i)"))
		}
		XCTAssertEqual(store.recentlyOpenedPaths.count, 10, "recents list must stay bounded")
		XCTAssertEqual(store.recentlyOpenedPaths.first, "extra11.md")
	}

    /// Titles are YAML double-quoted by `note new`, `props edit` and
    /// `MobileNoteWriter`; quotes and backslashes must read back unescaped.
    func testDoubleQuotedTitleEscapesReadBackUnescaped() throws {
        let root = URL(fileURLWithPath: "/tmp/SymDeskMobileVault", isDirectory: true)
        let title = "Käse \"Crème\" \\ 日本"
        let document = MobileNoteWriter.noteDocument(title: title, body: "body", createdAt: Date(timeIntervalSince1970: 1_752_600_000))
        let note = try MobileVaultParser.parse(
            data: Data(document.utf8), fileURL: root.appendingPathComponent("k.md"), root: root, modifiedAt: Date()
        )
        XCTAssertEqual(note.title, title)

        let single = "---\ntitle: 'it''s quoted'\ncorrespondent: \"ACME \\\"Ltd\\\"\"\n---\n\nbody"
        let parsed = try MobileVaultParser.parse(
            data: Data(single.utf8), fileURL: root.appendingPathComponent("s.md"), root: root, modifiedAt: Date()
        )
        XCTAssertEqual(parsed.title, "it's quoted")
        XCTAssertEqual(parsed.correspondent, "ACME \"Ltd\"")
    }
}
