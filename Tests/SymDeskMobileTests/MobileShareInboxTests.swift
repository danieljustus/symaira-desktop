import Foundation
import XCTest
@testable import SymDeskMobile

/// Tests for the Share Extension plumbing (#327): the App Group inbox
/// drainer routes URL/text shares into the note-create path (source in
/// frontmatter) and keeps PDFs/images on the ingest path, clears the
/// inbox oldest first without losing files on failure, and purges the
/// inbox when the vault is disconnected so shares never leak into
/// another vault.
final class MobileShareInboxTests: XCTestCase {

    private var tempDirectory: URL!

    override func setUpWithError() throws {
        tempDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("ShareTests-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: tempDirectory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: tempDirectory)
    }

    private func makeInbox() throws -> (MobileShareInbox, URL, MobileWriteCoordinator) {
        let inboxDir = tempDirectory.appendingPathComponent("ShareInbox", isDirectory: true)
        try FileManager.default.createDirectory(at: inboxDir, withIntermediateDirectories: true)
        let outbox = try MobileOutbox(directory: tempDirectory.appendingPathComponent("outbox", isDirectory: true))
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        let inbox = MobileShareInbox(coordinator: coordinator, inboxURL: inboxDir)
        return (inbox, inboxDir, coordinator)
    }

    // MARK: - Drain behaviour

    func testDrainMovesItemsIntoOutboxAndClearsInbox() async throws {
        let (inbox, inboxDir, coordinator) = try makeInbox()

        // Two shares, older first in name order: a URL descriptor (becomes
        // a note) and a PDF original (keeps the ingest path).
        try Data("url: https://example.com/a\n".utf8).write(to: inboxDir.appendingPathComponent("share-1000.url"))
        try Data("pdf-bytes".utf8).write(to: inboxDir.appendingPathComponent("share-2000.pdf"))

        XCTAssertEqual(inbox.pendingCount(), 2)
        await inbox.drain()

        XCTAssertEqual(inbox.pendingCount(), 0, "inbox must be empty after a successful drain")
        let entries = await coordinator.entries()
        XCTAssertEqual(entries.count, 2, "both shares must be queued in the write layer")
        XCTAssertEqual(entries[0].kind, .createNote, "URL shares become note creations")
        XCTAssertEqual(entries[0].path, "example.com_a.md")
        XCTAssertEqual(entries[1].kind, .uploadOriginal, "PDF shares keep the ingest path")
        XCTAssertEqual(entries[1].originalFilename, "2000.pdf")
        // No backend is set, so the entries stay queued in the outbox.
        XCTAssertTrue(entries.allSatisfy { $0.state == .queued })
    }

    func testURLShareBecomesNoteWithSourceFrontmatter() async throws {
        let (inbox, inboxDir, coordinator) = try makeInbox()
        // What the Share Extension writes for a Safari share (#327 AC3).
        try Data("url: https://example.com/a\n".utf8).write(to: inboxDir.appendingPathComponent("share-1000.url"))

        await inbox.drain()

        XCTAssertEqual(inbox.pendingCount(), 0)
        let entries = await coordinator.entries()
        XCTAssertEqual(entries.count, 1)
        XCTAssertEqual(entries[0].kind, .createNote)
        // Title is host + path, so repeated shares from one site stay distinct.
        XCTAssertEqual(entries[0].path, "example.com_a.md")
        let content = entries[0].content ?? ""
        XCTAssertTrue(content.contains("source: \"https://example.com/a\""),
                      "note frontmatter must record the source URL, got: \(content)")
        XCTAssertTrue(content.contains("https://example.com/a"), "note body must contain the shared URL")
    }

    func testTextShareBecomesNoteWithBody() async throws {
        let (inbox, inboxDir, coordinator) = try makeInbox()
        // What the Share Extension writes for a plain-text share.
        try Data("text: Hello from the share sheet\nSecond line\n".utf8)
            .write(to: inboxDir.appendingPathComponent("share-1000.txt"))

        await inbox.drain()

        let entries = await coordinator.entries()
        XCTAssertEqual(entries.count, 1)
        XCTAssertEqual(entries[0].kind, .createNote)
        XCTAssertEqual(entries[0].path, "Hello_from_the_share_sheet.md")
        let content = entries[0].content ?? ""
        XCTAssertTrue(content.contains("Hello from the share sheet\nSecond line"),
                      "shared text must become the note body, got: \(content)")
        XCTAssertFalse(content.contains("source:"), "plain text shares carry no source URL")
    }

    func testURLShareWithCommentAppendsCommentToBody() async throws {
        let (inbox, inboxDir, coordinator) = try makeInbox()
        // Comment is the first descriptor line (minimal share UI).
        try Data("comment: worth revisiting\nurl: https://example.com/a\n".utf8)
            .write(to: inboxDir.appendingPathComponent("share-1000.url"))

        await inbox.drain()

        let entries = await coordinator.entries()
        XCTAssertEqual(entries.count, 1)
        XCTAssertEqual(entries[0].kind, .createNote)
        let content = entries[0].content ?? ""
        XCTAssertTrue(content.contains("source: \"https://example.com/a\""))
        XCTAssertTrue(content.contains("worth revisiting"), "comment must be appended to the note body")
    }

    func testDrainKeepsFileWhenEnqueueFails() async throws {
        let (inbox, inboxDir, _) = try makeInbox()
        // Break payload storage so enqueuing an upload (which persists the
        // original bytes) fails; the inbox copy must survive for a retry.
        try Data("pdf-bytes".utf8).write(to: inboxDir.appendingPathComponent("share-1.pdf"))
        let payloadsDir = tempDirectory
            .appendingPathComponent("outbox", isDirectory: true)
            .appendingPathComponent("payloads", isDirectory: true)
        try? FileManager.default.removeItem(at: payloadsDir)

        await inbox.drain()

        XCTAssertEqual(inbox.pendingCount(), 1, "failed enqueue must leave the share in the inbox")
    }

    func testDrainEmptyInboxIsNoOp() async throws {
        let (inbox, _, _) = try makeInbox()
        XCTAssertEqual(inbox.pendingCount(), 0)
        await inbox.drain()
        XCTAssertEqual(inbox.pendingCount(), 0)
    }

    func testPendingCountReflectsInboxContents() async throws {
        let (inbox, inboxDir, _) = try makeInbox()
        try Data("a".utf8).write(to: inboxDir.appendingPathComponent("share-1.txt"))
        try Data("b".utf8).write(to: inboxDir.appendingPathComponent("share-2.jpg"))
        XCTAssertEqual(inbox.pendingCount(), 2)
        try? FileManager.default.removeItem(at: inboxDir.appendingPathComponent("share-1.txt"))
        XCTAssertEqual(inbox.pendingCount(), 1)
    }

    // MARK: - Purge on disconnect (#327 AC5)

    func testPurgeEmptiesInbox() async throws {
        let (inbox, inboxDir, _) = try makeInbox()
        try Data("url: https://example.com/a\n".utf8).write(to: inboxDir.appendingPathComponent("share-1.url"))
        try Data("pdf-bytes".utf8).write(to: inboxDir.appendingPathComponent("share-2.pdf"))
        XCTAssertEqual(inbox.pendingCount(), 2)

        inbox.purge()

        XCTAssertEqual(inbox.pendingCount(), 0, "purge must drop every queued share")
        XCTAssertFalse(FileManager.default.fileExists(atPath: inboxDir.appendingPathComponent("share-1.url").path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: inboxDir.appendingPathComponent("share-2.pdf").path))
    }

    func testPurgeEmptyInboxIsNoOp() async throws {
        let (inbox, _, _) = try makeInbox()
        XCTAssertEqual(inbox.pendingCount(), 0)
        inbox.purge()
        XCTAssertEqual(inbox.pendingCount(), 0)
    }

    // MARK: - Descriptor decoding

    func testNoteShareParsesDescriptorsOnly() throws {
        // URL descriptor.
        let urlShare = try XCTUnwrap(MobileShareInbox.noteShare(from: Data("url: https://example.com/a\n".utf8)))
        XCTAssertEqual(urlShare.source, "https://example.com/a")
        XCTAssertEqual(urlShare.title, "example.com/a")

        // Text descriptor.
        let textShare = try XCTUnwrap(MobileShareInbox.noteShare(from: Data("text: hello\nworld\n".utf8)))
        XCTAssertNil(textShare.source)
        XCTAssertEqual(textShare.body, "hello\nworld")

        // Comment line is stripped into the body.
        let commented = try XCTUnwrap(
            MobileShareInbox.noteShare(from: Data("comment: note this\nurl: https://example.com/a\n".utf8))
        )
        XCTAssertEqual(commented.source, "https://example.com/a")
        XCTAssertTrue(commented.body.contains("note this"))

        // Binary payloads (PDFs, images) are not descriptors.
        XCTAssertNil(MobileShareInbox.noteShare(from: Data([0x25, 0x50, 0x44, 0x46])))
        XCTAssertNil(MobileShareInbox.noteShare(from: Data("random text file".utf8)))
    }

    // MARK: - Filename normalisation

    func testShareFilenameNormalisedForIngest() throws {
        // "share-" prefix is stripped; spaces become underscores so the
        // ingest pipeline classifies by extension reliably.
        XCTAssertEqual(MobileShareInbox.filename(from: "share-1000.txt"), "1000.txt")
        XCTAssertEqual(MobileShareInbox.filename(from: "share-2000.pdf"), "2000.pdf")
        XCTAssertEqual(MobileShareInbox.filename(from: "share-3000.JPG"), "3000.JPG")
    }
}
