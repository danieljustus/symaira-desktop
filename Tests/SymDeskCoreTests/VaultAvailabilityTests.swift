import Foundation
import XCTest
@testable import SymDeskCore

/// A vault whose folder had been deleted was still restored on launch and
/// presented as a valid, empty vault — "0 Documents / 0 Notes" with an
/// invitation to create a note in a directory that no longer exists (issue
/// #444). These pin the availability decision that gates that restore.
final class VaultAvailabilityTests: XCTestCase {
    private var created: [URL] = []

    override func tearDownWithError() throws {
        for url in created { try? FileManager.default.removeItem(at: url) }
        created = []
    }

    func testExistingDirectoryIsAvailable() throws {
        let dir = try makeDirectory()
        XCTAssertFalse(
            VaultConfig.isVaultDirectoryMissing(dir.path),
            "an existing vault directory must still open normally"
        )
    }

    func testDeletedDirectoryIsReportedMissing() throws {
        let dir = try makeDirectory()
        try FileManager.default.removeItem(at: dir)

        XCTAssertTrue(
            VaultConfig.isVaultDirectoryMissing(dir.path),
            "a vault directory that no longer exists must be reported missing"
        )
    }

    func testRegularFileIsNotAcceptedAsAVault() throws {
        let dir = try makeDirectory()
        let file = dir.appendingPathComponent("not-a-vault")
        try Data("x".utf8).write(to: file)

        XCTAssertTrue(
            VaultConfig.isVaultDirectoryMissing(file.path),
            "a plain file must not be treated as an openable vault"
        )
    }

    func testEmptyPathIsReportedMissing() {
        XCTAssertTrue(VaultConfig.isVaultDirectoryMissing(""))
    }

    // MARK: - Helpers

    private func makeDirectory() throws -> URL {
        let dir = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("vault-availability-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        created.append(dir)
        return dir
    }
}
