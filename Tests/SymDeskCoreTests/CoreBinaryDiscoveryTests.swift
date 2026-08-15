import Darwin
import XCTest
@testable import SymDeskCore
import SymairaCLIRunner
import SymairaToolKit

/// Contract tests for locating the `symdesk` core binary.
///
/// The strict provenance search rejects any directory that is group- or
/// world-writable. Homebrew's Apple Silicon prefix (`/opt/homebrew/bin`) is
/// group-writable by design, so a standard `brew install` is invisible to the
/// strict search and the app cannot start at all (#437). These tests pin the
/// acceptance decision for such a directory, and prove the strict search is
/// still preferred wherever it succeeds.
@MainActor
final class CoreBinaryDiscoveryTests: XCTestCase {
    private var createdDirectories: [URL] = []

    override func tearDownWithError() throws {
        for url in createdDirectories {
            try? FileManager.default.removeItem(at: url)
        }
        createdDirectories = []
    }

    // MARK: - Reproduction

    func testStrictSearchRejectsGroupWritableDirectory() async throws {
        let directory = try makeBinDirectory(mode: 0o775, containingFixture: true)
        let detector = ToolDetector(locator: locator(for: directory))

        let detected = await detector.detect(Self.fixtureTool)

        XCTAssertNil(
            detected,
            "strict provenance search is expected to reject the group-writable directory \(directory.path) — this is the condition a standard Homebrew prefix meets"
        )
    }

    // MARK: - Fallback behavior

    func testDetectionFallsBackForGroupWritableHomebrewStylePrefix() async throws {
        let directory = try makeBinDirectory(mode: 0o775, containingFixture: true)

        let detection = await CoreBinaryDiscovery.detect(
            Self.fixtureTool,
            strict: ToolDetector(locator: locator(for: directory)),
            relaxed: ToolDetector(locator: locator(for: directory), allowUnverified: true)
        )

        let resolved = try XCTUnwrap(detection, "a binary in a group-writable directory must still be found")
        XCTAssertEqual(resolved.tool.location.url.deletingLastPathComponent().path, directory.path)
        let note = try XCTUnwrap(resolved.provenanceNote, "falling back must record why the strict search was skipped")
        XCTAssertTrue(
            note.contains(directory.path),
            "the note must name the directory it accepted, got: \(note)"
        )
    }

    func testDetectionPrefersStrictSearchForSecureDirectory() async throws {
        let directory = try makeBinDirectory(mode: 0o755, containingFixture: true)

        let detection = await CoreBinaryDiscovery.detect(
            Self.fixtureTool,
            strict: ToolDetector(locator: locator(for: directory)),
            relaxed: ToolDetector(locator: locator(for: directory), allowUnverified: true)
        )

        let resolved = try XCTUnwrap(detection, "a binary in a secure directory must be found")
        XCTAssertNil(
            resolved.provenanceNote,
            "a directory that passes the strict search must not be reported as a fallback"
        )
    }

    func testDetectionReturnsNilWhenBinaryIsAbsent() async throws {
        let directory = try makeBinDirectory(mode: 0o755, containingFixture: false)

        let detection = await CoreBinaryDiscovery.detect(
            Self.fixtureTool,
            strict: ToolDetector(locator: locator(for: directory)),
            relaxed: ToolDetector(locator: locator(for: directory), allowUnverified: true)
        )

        XCTAssertNil(detection, "an empty search path must stay 'not installed' rather than falling back to nothing")
    }

    // MARK: - Helpers

    private static let fixtureBinaryName = "symdesk-fixture"

    private static let fixtureTool = SymairaTool(
        id: "symdesk-fixture",
        displayName: "SymDesk Fixture",
        binaryName: fixtureBinaryName,
        homebrewFormula: "symdesk"
    )

    /// A locator restricted to `directory`, so only the PATH pass runs and the
    /// directory-provenance decision is the only variable under test.
    private func locator(for directory: URL) -> BinaryLocator {
        BinaryLocator(
            bundle: nil,
            userOverride: nil,
            searchPATH: directory.path,
            extraDirectories: []
        )
    }

    private func makeBinDirectory(mode: mode_t, containingFixture: Bool) throws -> URL {
        let directory = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("core-discovery-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        createdDirectories.append(directory)

        if containingFixture {
            let source = URL(fileURLWithPath: #filePath)
                .deletingLastPathComponent()
                .appendingPathComponent("Fixtures/fake-symdesk.sh")
            let destination = directory.appendingPathComponent(Self.fixtureBinaryName)
            try FileManager.default.copyItem(at: source, to: destination)
            try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: destination.path)
        }

        // Set the directory mode last: copying into a 0o775 directory is fine,
        // but the mode must be the one under test when the locator runs.
        try FileManager.default.setAttributes([.posixPermissions: mode], ofItemAtPath: directory.path)
        return directory
    }
}
