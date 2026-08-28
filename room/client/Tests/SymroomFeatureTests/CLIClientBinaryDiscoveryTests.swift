import XCTest
import SymairaToolKit
@testable import SymroomKit

/// Fake `SymroomLocating` conformer so these tests are hermetic: they never
/// depend on whether the developer's machine actually has `symroom` on PATH
/// or in a Homebrew prefix (#608 was invisible on exactly such a machine —
/// a strict-search-only test would have hidden the regression).
private struct FakeLocator: SymroomLocating {
    let strictResult: BinaryLocator.Located?
    let relaxedResult: BinaryLocator.Located?

    func locate(_ binaryName: String, allowUnverified: Bool) -> BinaryLocator.Located? {
        allowUnverified ? relaxedResult : strictResult
    }
}

/// Covers the strict-then-relaxed resolution strategy `RoomCLIClient` adopted
/// from `CoreBinaryDiscovery` (#437, #608): strict is preferred, relaxed is
/// only a fallback, and the fallback is never silent.
final class CLIClientBinaryDiscoveryTests: XCTestCase {
    private var tempDir: URL!
    private var fakeExecutable: URL!

    override func setUpWithError() throws {
        tempDir = FileManager.default.temporaryDirectory
            .appendingPathComponent("symroom-cli-tests-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: tempDir, withIntermediateDirectories: true)

        fakeExecutable = tempDir.appendingPathComponent("symroom")
        let script = "#!/bin/sh\necho '{}'\n"
        try script.write(to: fakeExecutable, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: fakeExecutable.path)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: tempDir)
    }

    private func located(source: BinaryLocator.Source, verified: Bool) -> BinaryLocator.Located {
        BinaryLocator.Located(url: fakeExecutable, source: source, verified: verified)
    }

    func testStrictHitReportsInstalledWithoutProvenanceNote() {
        let locator = FakeLocator(
            strictResult: located(source: .extraDirectory, verified: true),
            relaxedResult: nil
        )
        let client = RoomCLIClient(locator: locator)

        XCTAssertTrue(client.isInstalled)
        XCTAssertNil(client.provenanceNote)
        XCTAssertNil(client.provenanceDirectory)
    }

    func testStrictMissRelaxedHitReportsInstalledWithProvenanceNote() {
        let locator = FakeLocator(
            strictResult: nil,
            relaxedResult: located(source: .extraDirectory, verified: false)
        )
        let client = RoomCLIClient(locator: locator)

        XCTAssertTrue(client.isInstalled)
        XCTAssertNotNil(client.provenanceNote)
        XCTAssertTrue(client.provenanceNote?.contains(tempDir.path) ?? false)
        XCTAssertTrue(client.provenanceNote?.contains("group- or world-writable") ?? false)
        XCTAssertEqual(client.provenanceDirectory, tempDir.path)
    }

    func testBothMissReportsNotInstalled() {
        let locator = FakeLocator(strictResult: nil, relaxedResult: nil)
        let client = RoomCLIClient(locator: locator)

        XCTAssertFalse(client.isInstalled)
        XCTAssertNil(client.provenanceNote)
        XCTAssertNil(client.provenanceDirectory)
    }
}
