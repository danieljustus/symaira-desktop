import Foundation
import XCTest
@testable import SymDeskMobile

/// Tests for the iOS composer (#325): draft persistence (autosave
/// survives termination), frontmatter/filename conformance on create,
/// raw-source preservation on edit, and write-layer routing.
final class MobileComposerTests: XCTestCase {

    private var tempDirectory: URL!

    override func setUpWithError() throws {
        tempDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("ComposerTests-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: tempDirectory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: tempDirectory)
    }

    private func makeDraftStore() throws -> MobileDraftStore {
        try MobileDraftStore(directory: tempDirectory.appendingPathComponent("drafts", isDirectory: true))
    }

    // MARK: - Draft persistence (force-quit survival)

    func testDraftSurvivesStoreRecreation() async throws {
        let store = try makeDraftStore()
        let draft = MobileDraftStore.Draft(
            id: "new-123",
            title: "Einkaufsliste",
            body: "Milch\nEier\nBrot",
            existingPath: nil,
            folder: nil,
            updatedAt: Date(timeIntervalSince1970: 42)
        )
        try await store.save(draft)

        // Simulate force-quit: a fresh store over the same directory.
        let reloaded = try makeDraftStore()
        let loaded = try await reloaded.load(id: "new-123")
        XCTAssertEqual(loaded?.title, "Einkaufsliste")
        XCTAssertEqual(loaded?.body, "Milch\nEier\nBrot")
        XCTAssertEqual(loaded?.updatedAt, Date(timeIntervalSince1970: 42))
    }

    func testDraftListSortedNewestFirst() async throws {
        let store = try makeDraftStore()
        let older = MobileDraftStore.Draft(id: "new-a", title: "Alt", body: "x", existingPath: nil, folder: nil, updatedAt: Date(timeIntervalSince1970: 100))
        let newer = MobileDraftStore.Draft(id: "new-b", title: "Neu", body: "y", existingPath: nil, folder: nil, updatedAt: Date(timeIntervalSince1970: 200))
        try await store.save(older)
        try await store.save(newer)

        let all = try await store.all()
        XCTAssertEqual(all.map(\.id), ["new-b", "new-a"])
    }

    func testDraftDeleteRemovesFile() async throws {
        let store = try makeDraftStore()
        let draft = MobileDraftStore.Draft(id: "new-x", title: "T", body: "b", existingPath: nil, folder: nil, updatedAt: .now)
        try await store.save(draft)
        try await store.delete(id: "new-x")
        let loaded = try await store.load(id: "new-x")
        XCTAssertNil(loaded)
        let all = try await store.all()
        XCTAssertTrue(all.isEmpty)
    }

    // MARK: - Create path (frontmatter + naming conformance)

    func testCreateNoteProducesDesktopConformantDocument() async throws {
        let outbox = try MobileOutbox(directory: tempDirectory.appendingPathComponent("outbox", isDirectory: true))
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        let vaultRoot = tempDirectory.appendingPathComponent("vault", isDirectory: true)
        try FileManager.default.createDirectory(at: vaultRoot, withIntermediateDirectories: true)
        await coordinator.setMode(MobileFilesWriteAdapter(vaultRoot: vaultRoot))

        // Exactly what MobileComposerView.finish() does for a new note.
        let filename = MobileNoteWriter.filename(for: "Einkaufsliste")
        XCTAssertEqual(filename, "Einkaufsliste.md")
        let content = MobileNoteWriter.noteDocument(title: "Einkaufsliste", body: "Milch\nEier")
        let entry = MobileOutboxEntry(kind: .createNote, path: filename, content: content)
        try await coordinator.enqueue(entry)

        // Wait for the drain to apply the write.
        for _ in 0..<250 {
            if try FileManager.default.fileExists(atPath: vaultRoot.appendingPathComponent(filename).path) {
                break
            }
            try? await Task.sleep(for: .milliseconds(20))
        }

        let written = try String(contentsOf: vaultRoot.appendingPathComponent(filename), encoding: .utf8)
        XCTAssertTrue(written.hasPrefix("---\ntitle: \"Einkaufsliste\"\n"), "contract-v6-compatible frontmatter expected, got: \(written)")

        // The desktop parser contract: frontmatter round-trips losslessly.
        let parsed = try MobileVaultParser.parse(
            data: Data(written.utf8),
            fileURL: vaultRoot.appendingPathComponent(filename),
            root: vaultRoot,
            modifiedAt: .now
        )
        XCTAssertEqual(parsed.title, "Einkaufsliste")
        XCTAssertEqual(parsed.body, "Milch\nEier")
    }

    func testCreateNoteInTargetFolder() async throws {
        let outbox = try MobileOutbox(directory: tempDirectory.appendingPathComponent("outbox", isDirectory: true))
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        let vaultRoot = tempDirectory.appendingPathComponent("vault", isDirectory: true)
        try FileManager.default.createDirectory(at: vaultRoot, withIntermediateDirectories: true)
        await coordinator.setMode(MobileFilesWriteAdapter(vaultRoot: vaultRoot))

        let filename = MobileNoteWriter.filename(for: "Notiz")
        let path = "notizen/" + filename
        let content = MobileNoteWriter.noteDocument(title: "Notiz", body: "Inhalt")
        try await coordinator.enqueue(MobileOutboxEntry(kind: .createNote, path: path, content: content))

        for _ in 0..<250 {
            if try FileManager.default.fileExists(atPath: vaultRoot.appendingPathComponent(path).path) {
                break
            }
            try? await Task.sleep(for: .milliseconds(20))
        }
        XCTAssertTrue(try FileManager.default.fileExists(atPath: vaultRoot.appendingPathComponent(path).path))
    }

    // MARK: - Edit path (raw source preservation)

    func testEditPreservesRawSourceThroughOutbox() async throws {
        let vaultRoot = tempDirectory.appendingPathComponent("vault", isDirectory: true)
        try FileManager.default.createDirectory(at: vaultRoot, withIntermediateDirectories: true)
        // A note with Markdown the mobile editor does not render.
        let original = """
        ---
        title: "Meeting"
        tags:
          - work
        ---

        # Agenda

        - [ ] Punkt 1
        - [ ] Punkt 2

        | Spalte A | Spalte B |
        | --- | --- |
        | 1 | 2 |

        [[wikilink]] und ![[](bild.png)
        """
        let target = vaultRoot.appendingPathComponent("meeting.md")
        try Data(original.utf8).write(to: target)

        let note = try MobileVaultParser.parse(
            data: Data(original.utf8),
            fileURL: target,
            root: vaultRoot,
            modifiedAt: .now
        )
        // Composer edit mode starts from rawContent.
        XCTAssertEqual(note.rawContent, original)

        let outbox = try MobileOutbox(directory: tempDirectory.appendingPathComponent("outbox", isDirectory: true))
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        await coordinator.setMode(MobileFilesWriteAdapter(vaultRoot: vaultRoot))

        // User appends a line; rawContent is what the composer submits.
        let edited = original + "\nNeue Zeile am Ende."
        // Precondition like MobileVaultStore.enqueueUpdateNote: stat the
        // file for mtime+size, not the parser's synthetic value.
        let values = try target.resourceValues(forKeys: [.contentModificationDateKey, .fileSizeKey])
        let precondition = MobileWritePrecondition(
            modifiedAt: values.contentModificationDate,
            size: values.fileSize
        )
        try await coordinator.enqueue(MobileOutboxEntry(
            kind: .updateNote,
            path: note.path,
            content: edited,
            precondition: precondition
        ))

        for _ in 0..<250 {
            if let current = try? String(contentsOf: vaultRoot.appendingPathComponent("meeting.md"), encoding: .utf8),
               current.contains("Neue Zeile am Ende.") {
                break
            }
            try? await Task.sleep(for: .milliseconds(20))
        }

        let written = try String(contentsOf: vaultRoot.appendingPathComponent("meeting.md"), encoding: .utf8)
        XCTAssertEqual(written, edited, "raw source must be preserved verbatim on edit")
        XCTAssertTrue(written.contains("- [ ] Punkt 1"))
        XCTAssertTrue(written.contains("| Spalte A | Spalte B |"))
        XCTAssertTrue(written.contains("[[wikilink]]"))
    }
}
