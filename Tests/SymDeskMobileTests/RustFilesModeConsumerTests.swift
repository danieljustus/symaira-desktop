import Foundation
import XCTest
@testable import SymDeskMobile

/// SWIFT-003 (docs/rust-port/contract-matrix.md): the iOS Files/iCloud
/// consumer has no CLI process, so compatibility with the Rust core is proven
/// through the shared directory in both directions.
///
/// * `testWriteFilesModeVault` — the iOS write layer (`MobileNoteWriter`,
///   `MobileFilesWriteAdapter` through `MobileWriteCoordinator`, outbox,
///   coordinated writes, mtime+size preconditions, conflict copies, consume
///   folder uploads) writes into a host-visible directory and records a
///   manifest of what it wrote. `scripts/rust-port/swift-consumer.sh` then runs
///   the Rust `symdesk` over that directory (`ls`, `index`, `search`,
///   `props get`, `conflict resolve`) and compares it with the manifest.
/// * `testReadRustWrittenVault` — after the script made Rust write notes
///   (`note new`, `props edit`, `note daily`, `conflict resolve`), the iOS
///   scanner/parser reads the same directory and must see identical titles,
///   tags, frontmatter values and the resolved conflict outcome.
///
/// Gated on `SYMDESK_CONSUMER_FILES_PHASE` (`write` | `read`),
/// `SYMDESK_CONSUMER_FILES_DIR` and `SYMDESK_CONSUMER_FILES_MANIFEST`
/// (`TEST_RUNNER_` prefix). A phase that does not match a test skips it; the
/// script asserts the executed-test count per phase from the xcresult.
final class RustFilesModeConsumerTests: XCTestCase {
    private struct Environment {
        let phase: String
        let directory: URL
        let manifest: URL
    }

    private func environment(phase wanted: String) throws -> Environment {
        let values = ProcessInfo.processInfo.environment
        guard let phase = values["SYMDESK_CONSUMER_FILES_PHASE"], !phase.isEmpty,
              let dir = values["SYMDESK_CONSUMER_FILES_DIR"], !dir.isEmpty,
              let manifest = values["SYMDESK_CONSUMER_FILES_MANIFEST"], !manifest.isEmpty else {
            throw XCTSkip("SYMDESK_CONSUMER_FILES_* are not set; use scripts/rust-port/swift-consumer.sh")
        }
        guard phase == wanted else { throw XCTSkip("phase \(phase) != \(wanted)") }
        var isDirectory: ObjCBool = false
        XCTAssertTrue(FileManager.default.fileExists(atPath: dir, isDirectory: &isDirectory) && isDirectory.boolValue,
                      "vault directory \(dir) must exist")
        return Environment(phase: phase, directory: URL(fileURLWithPath: dir, isDirectory: true),
                           manifest: URL(fileURLWithPath: manifest))
    }

    private func waitForEmptyQueue(_ coordinator: MobileWriteCoordinator) async throws {
        for _ in 0..<600 {
            if await coordinator.entries().isEmpty { return }
            try await Task.sleep(for: .milliseconds(50))
        }
        let left = await coordinator.entries().map { "\($0.path):\($0.state.rawValue):\($0.lastError ?? "-")" }
        XCTFail("outbox did not drain: \(left)")
    }

    private func waitForFailedEntry(_ coordinator: MobileWriteCoordinator) async throws -> MobileOutboxEntry {
        for _ in 0..<600 {
            if let failed = await coordinator.failedEntries().first { return failed }
            try await Task.sleep(for: .milliseconds(50))
        }
        let all = await coordinator.entries().map { "\($0.path):\($0.state.rawValue):\($0.lastError ?? "-")" }
        XCTFail("no failed entry: \(all)")
        throw CocoaError(.fileReadUnknown)
    }

    // MARK: - iOS writes → Rust reads

    func testWriteFilesModeVault() async throws {
        let env = try environment(phase: "write")
        let root = env.directory
        let outboxDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("files-mode-outbox-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: outboxDirectory) }

        let outbox = try MobileOutbox(directory: outboxDirectory)
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        await coordinator.setMode(MobileFilesWriteAdapter(vaultRoot: root))
        let scanner = MobileVaultScanner()

        struct Written: Codable { var path: String; var title: String; var tags: [String]; var created: String; var bodyContains: String }
        var written: [Written] = []

        func create(title: String, body: String, folder: String? = nil) async throws -> (path: String, document: String) {
            let filename = MobileNoteWriter.filename(for: title)
            let path = folder.map { ($0 as NSString).appendingPathComponent(filename) } ?? filename
            let created = Date(timeIntervalSince1970: 1_752_600_000)
            let document = MobileNoteWriter.noteDocument(title: title, body: body, createdAt: created)
            try await coordinator.enqueue(MobileOutboxEntry(kind: .createNote, path: path, content: document, folder: folder))
            try await waitForEmptyQueue(coordinator)
            // Byte-for-byte what the write layer produced is what is on disk.
            let onDisk = try String(contentsOf: root.appendingPathComponent(path), encoding: .utf8)
            XCTAssertEqual(onDisk, document)
            written.append(Written(path: path, title: title, tags: [], created: ISO8601DateFormatter().string(from: created), bodyContains: body))
            return (path, document)
        }

        // 1. plain, unicode+escaping (the committed mobile-writer.json shape) and nested-folder creates
        let simple = try await create(title: "iOS Simple", body: "Quokka body from the phone")
        _ = try await create(title: "Käse \"Crème\" \\ 日本", body: "## Einkauf\n\nMilch und 日本語")
        _ = try await create(title: "Nested iOS", body: "inside a folder", folder: "ios-folder")

        // 2. update with the precondition (mtime + size) captured from a scan
        let snapshot = try await scanner.scan(root: root)
        let seen = try XCTUnwrap(snapshot.notes.first { $0.path == simple.path })
        let attributes = try root.appendingPathComponent(simple.path).resourceValues(forKeys: [.contentModificationDateKey, .fileSizeKey])
        let precondition = MobileWritePrecondition(modifiedAt: attributes.contentModificationDate, size: attributes.fileSize)
        XCTAssertEqual(seen.fileSize, attributes.fileSize)
        let updatedDocument = MobileNoteWriter.noteDocument(
            title: "iOS Simple", body: "Quokka body EDITED on the phone", createdAt: Date(timeIntervalSince1970: 1_752_600_000)
        )
        try await coordinator.enqueue(MobileOutboxEntry(kind: .updateNote, path: simple.path, content: updatedDocument, precondition: precondition))
        try await waitForEmptyQueue(coordinator)
        XCTAssertEqual(try String(contentsOf: root.appendingPathComponent(simple.path), encoding: .utf8), updatedDocument)
        written[0].bodyContains = "Quokka body EDITED on the phone"

        // 3. stale precondition → local version preserved as a conflicted copy, remote untouched
        let conflictingDocument = MobileNoteWriter.noteDocument(
            title: "iOS Simple", body: "Quokka LOCAL OFFLINE edit", createdAt: Date(timeIntervalSince1970: 1_752_600_000)
        )
        try await coordinator.enqueue(MobileOutboxEntry(kind: .updateNote, path: simple.path, content: conflictingDocument, precondition: precondition))
        let failed = try await waitForFailedEntry(coordinator)
        let conflictPath = "iOS_Simple conflicted copy.md"
        XCTAssertEqual(failed.conflictPath, conflictPath)
        XCTAssertEqual(try String(contentsOf: root.appendingPathComponent(simple.path), encoding: .utf8), updatedDocument)
        XCTAssertEqual(try String(contentsOf: root.appendingPathComponent(conflictPath), encoding: .utf8), conflictingDocument)
        try await coordinator.remove(id: failed.id)

        // 4. upload original into the consume folder the Rust watcher ingests from
        let pdf = Data("%PDF-1.4 files-mode upload".utf8)
        try await coordinator.enqueue(MobileOutboxEntry(
            kind: .uploadOriginal, path: "scan.pdf", originalData: pdf, originalFilename: "scan.pdf"
        ))
        try await waitForEmptyQueue(coordinator)
        XCTAssertEqual(try Data(contentsOf: root.appendingPathComponent("inbox_watch/scan.pdf")), pdf)

        // 5. the iOS parser reads back what the writer produced
        let final = try await scanner.scan(root: root)
        XCTAssertEqual(final.skippedFiles, 0)
        for note in written {
            let parsed = try XCTUnwrap(final.notes.first { $0.path == note.path }, "scanner missed \(note.path)")
            XCTAssertEqual(parsed.title, note.title)
            XCTAssertEqual(parsed.tags, note.tags)
            XCTAssertEqual(parsed.created, note.created)
            XCTAssertTrue(parsed.body.contains(note.bodyContains), parsed.body)
        }

        struct Manifest: Codable {
            var notes: [Written]
            var conflict: [String: String]
            var upload: String
        }
        let manifest = Manifest(
            notes: written,
            conflict: [
                "original": simple.path,
                "copy": conflictPath,
                "original_body": "Quokka body EDITED on the phone",
                "copy_body": "Quokka LOCAL OFFLINE edit"
            ],
            upload: "inbox_watch/scan.pdf"
        )
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        try encoder.encode(manifest).write(to: env.manifest, options: .atomic)
    }

    // MARK: - Rust writes → iOS reads

    func testReadRustWrittenVault() async throws {
        let env = try environment(phase: "read")
        struct Expected: Decodable {
            struct Note: Decodable {
                var path: String
                var title: String
                var tags: [String]
                var status: String?
                var bodyContains: String?
                enum CodingKeys: String, CodingKey { case path, title, tags, status, bodyContains = "body_contains" }
            }
            var notes: [Note]
            var absent: [String]
        }
        let expected = try JSONDecoder().decode(Expected.self, from: Data(contentsOf: env.manifest))
        XCTAssertFalse(expected.notes.isEmpty, "the script must provide Rust-written notes")

        let snapshot = try await MobileVaultScanner().scan(root: env.directory)
        XCTAssertEqual(snapshot.skippedFiles, 0, "every Rust-written/resolved note must parse")

        for note in expected.notes {
            // Normalise to NFC: APFS may hand back decomposed names.
            let parsed = try XCTUnwrap(
                snapshot.notes.first { $0.path.precomposedStringWithCanonicalMapping == note.path.precomposedStringWithCanonicalMapping },
                "iOS scanner missed Rust-written note \(note.path); have \(snapshot.notes.map(\.path))"
            )
            XCTAssertEqual(parsed.title, note.title, note.path)
            XCTAssertEqual(parsed.tags, note.tags, note.path)
            XCTAssertFalse(parsed.created.isEmpty, "created frontmatter must survive: \(note.path)")
            if let status = note.status { XCTAssertEqual(parsed.status, status, note.path) }
            if let fragment = note.bodyContains { XCTAssertTrue(parsed.body.contains(fragment), "\(note.path): \(parsed.body)") }
        }
        for gone in expected.absent {
            XCTAssertFalse(snapshot.notes.contains { $0.path == gone }, "\(gone) must be gone after Rust `conflict resolve`")
        }
    }
}
