import XCTest
import SymairaToolKit
@testable import SymroomKit

/// Locator that answers per binary name, so the symdesk-first resolution and
/// the standalone `symroom` fallback can be told apart.
private struct NamedLocator: SymroomLocating {
    let strict: [String: BinaryLocator.Located]
    let relaxed: [String: BinaryLocator.Located]

    func locate(_ binaryName: String, allowUnverified: Bool) -> BinaryLocator.Located? {
        allowUnverified ? relaxed[binaryName] : strict[binaryName]
    }
}

/// The project journal lives in `symdesk room ...`; a standalone `symroom` is
/// only a fallback. Fake executables record their arguments to a file.
final class CLIClientCommandPrefixTests: XCTestCase {
    private var tempDir: URL!

    override func setUpWithError() throws {
        tempDir = FileManager.default.temporaryDirectory
            .appendingPathComponent("symroom-prefix-tests-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: tempDir, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: tempDir)
    }

    /// Writes an executable named `name` that records its arguments and prints `[]`.
    private func makeRecordingBinary(_ name: String) throws -> (located: BinaryLocator.Located, log: URL) {
        let log = tempDir.appendingPathComponent("\(name).args")
        let url = tempDir.appendingPathComponent(name)
        let script = "#!/bin/sh\necho \"$@\" > '\(log.path)'\necho '[]'\n"
        try script.write(to: url, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
        return (BinaryLocator.Located(url: url, source: .extraDirectory, verified: true), log)
    }

    func testSymdeskIsPreferredAndGetsRoomPrepended() async throws {
        let symdesk = try makeRecordingBinary("symdesk")
        let symroom = try makeRecordingBinary("symroom")
        let client = RoomCLIClient(locator: NamedLocator(
            strict: ["symdesk": symdesk.located, "symroom": symroom.located],
            relaxed: [:]
        ))

        XCTAssertTrue(client.isInstalled)
        XCTAssertEqual(client.commandPrefix, ["room"])
        _ = try await client.listMembers(in: tempDir.path)
        let recorded = try String(contentsOf: symdesk.log, encoding: .utf8)
        XCTAssertEqual(recorded.trimmingCharacters(in: .whitespacesAndNewlines), "room member list --json")
        XCTAssertFalse(FileManager.default.fileExists(atPath: symroom.log.path))
    }

    func testStandaloneSymroomFallbackKeepsPlainArguments() async throws {
        let symroom = try makeRecordingBinary("symroom")
        let client = RoomCLIClient(locator: NamedLocator(strict: ["symroom": symroom.located], relaxed: [:]))

        XCTAssertTrue(client.isInstalled)
        XCTAssertEqual(client.commandPrefix, [])
        _ = try await client.listMembers(in: tempDir.path)
        let recorded = try String(contentsOf: symroom.log, encoding: .utf8)
        XCTAssertEqual(recorded.trimmingCharacters(in: .whitespacesAndNewlines), "member list --json")
    }

    func testStrictSymroomBeatsRelaxedSymdesk() throws {
        let symdesk = try makeRecordingBinary("symdesk")
        let symroom = try makeRecordingBinary("symroom")
        let client = RoomCLIClient(locator: NamedLocator(
            strict: ["symroom": symroom.located],
            relaxed: ["symdesk": symdesk.located]
        ))

        XCTAssertEqual(client.commandPrefix, [])
        XCTAssertNil(client.provenanceNote)
    }

    func testRelaxedSymdeskNamesTheBinaryInTheProvenanceNote() throws {
        let symdesk = try makeRecordingBinary("symdesk")
        let client = RoomCLIClient(locator: NamedLocator(strict: [:], relaxed: ["symdesk": symdesk.located]))

        XCTAssertEqual(client.commandPrefix, ["room"])
        XCTAssertTrue(client.provenanceNote?.hasPrefix("Loaded symdesk from") ?? false)
        XCTAssertEqual(client.provenanceDirectory, tempDir.path)
    }

    func testNotFoundMessageNamesSymdesk() {
        let message = RoomCLIError.binaryNotFound.errorDescription ?? ""
        XCTAssertTrue(message.contains("symdesk"))
        XCTAssertTrue(message.contains("symdesk room"))
    }
}
