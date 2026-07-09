import XCTest
@testable import SymDeskCore

private final class FakeProcessRunner: UpdateProcessRunner, @unchecked Sendable {
    var dittoStatus: Int32 = 0
    var verifyStatus: Int32 = 0
    var teamIdentifierOutput = "TeamIdentifier=ABCDE12345"
    var calls: [[String]] = []

    func run(_ launchPath: String, _ arguments: [String]) throws -> (status: Int32, output: String) {
        calls.append([launchPath] + arguments)

        if launchPath == "/usr/bin/ditto" {
            if dittoStatus == 0 {
                let destDir = URL(fileURLWithPath: arguments[3])
                let appDir = destDir.appendingPathComponent("SymDesk.app")
                try? FileManager.default.createDirectory(at: appDir, withIntermediateDirectories: true)
            }
            return (dittoStatus, "")
        }

        if launchPath == "/usr/bin/codesign" {
            if arguments.contains("--verify") {
                return (verifyStatus, verifyStatus == 0 ? "valid" : "invalid signature")
            }
            if arguments.contains("-dvv") {
                return (0, teamIdentifierOutput)
            }
        }
        return (0, "")
    }
}

private final class FakeFileSystem: UpdateFileSystem, @unchecked Sendable {
    var writable = true
    var moves: [(URL, URL)] = []
    var failSecondMove = false
    private var existing: Set<String> = []

    func fileExists(atPath path: String) -> Bool {
        existing.contains(path)
    }

    func isWritableFile(atPath path: String) -> Bool {
        writable
    }

    func moveItem(at src: URL, to dst: URL) throws {
        moves.append((src, dst))
        if failSecondMove && moves.count == 2 {
            throw CocoaError(.fileWriteUnknown)
        }
        existing.remove(src.path)
        existing.insert(dst.path)
    }

    func removeItem(at url: URL) throws {
        existing.remove(url.path)
    }
}

private struct FakeDownloader: UpdateAssetDownloader {
    var shouldFail = false

    func download(from url: URL) async throws -> URL {
        if shouldFail {
            throw UpdateInstallError.downloadFailed
        }
        let tmp = FileManager.default.temporaryDirectory.appendingPathComponent("fake-\(UUID().uuidString).zip")
        try Data().write(to: tmp)
        return tmp
    }
}

private final class FakeRelauncher: AppRelauncher, @unchecked Sendable {
    var relaunched: URL?

    func relaunch(appURL: URL) {
        relaunched = appURL
    }
}

private final class StageCollector: @unchecked Sendable {
    private(set) var stages: [InstallStage] = []

    func record(_ stage: InstallStage) {
        stages.append(stage)
    }
}

final class UpdateInstallerTests: XCTestCase {
    private let downloadURL = URL(string: "https://example.com/SymDesk.zip")!
    private let installedAppURL = URL(fileURLWithPath: "/Applications/SymDesk.app")

    func testTranslocatedAppAbortsBeforeDownload() async {
        let translocatedURL = URL(fileURLWithPath: "/private/var/folders/xy/AppTranslocation/abc/d.app/SymDesk.app")
        let downloader = FakeDownloader()

        do {
            try await UpdateInstaller.performInstall(
                downloadURL: downloadURL,
                installedAppURL: translocatedURL,
                expectedTeamIdentifier: nil,
                downloader: downloader
            )
            XCTFail("expected appTranslocated error")
        } catch UpdateInstallError.appTranslocated {
            // expected
        } catch {
            XCTFail("unexpected error: \(error)")
        }
    }

    func testNonWritableInstallLocationAborts() async {
        let fileSystem = FakeFileSystem()
        fileSystem.writable = false

        do {
            try await UpdateInstaller.performInstall(
                downloadURL: downloadURL,
                installedAppURL: installedAppURL,
                expectedTeamIdentifier: nil,
                fileSystem: fileSystem
            )
            XCTFail("expected installLocationNotWritable error")
        } catch UpdateInstallError.installLocationNotWritable {
            // expected
        } catch {
            XCTFail("unexpected error: \(error)")
        }
    }

    func testSignatureVerificationFailureAbortsWithoutSwap() async {
        let fileSystem = FakeFileSystem()
        let processRunner = FakeProcessRunner()
        processRunner.verifyStatus = 1

        do {
            try await UpdateInstaller.performInstall(
                downloadURL: downloadURL,
                installedAppURL: installedAppURL,
                expectedTeamIdentifier: nil,
                fileSystem: fileSystem,
                processRunner: processRunner,
                downloader: FakeDownloader()
            )
            XCTFail("expected signatureVerificationFailed error")
        } catch UpdateInstallError.signatureVerificationFailed {
            XCTAssertTrue(fileSystem.moves.isEmpty, "must not touch the installed app on failed verification")
        } catch {
            XCTFail("unexpected error: \(error)")
        }
    }

    func testTeamIdentifierMismatchAbortsWithoutSwap() async {
        let fileSystem = FakeFileSystem()
        let processRunner = FakeProcessRunner()
        processRunner.teamIdentifierOutput = "TeamIdentifier=WRONGTEAM"

        do {
            try await UpdateInstaller.performInstall(
                downloadURL: downloadURL,
                installedAppURL: installedAppURL,
                expectedTeamIdentifier: "ABCDE12345",
                fileSystem: fileSystem,
                processRunner: processRunner,
                downloader: FakeDownloader()
            )
            XCTFail("expected teamIdentifierMismatch error")
        } catch UpdateInstallError.teamIdentifierMismatch {
            XCTAssertTrue(fileSystem.moves.isEmpty)
        } catch {
            XCTFail("unexpected error: \(error)")
        }
    }

    func testNilExpectedTeamIdentifierNoOpsTeamCheck() async throws {
        let fileSystem = FakeFileSystem()
        let processRunner = FakeProcessRunner()
        processRunner.teamIdentifierOutput = "TeamIdentifier=not set"
        let relauncher = FakeRelauncher()

        try await UpdateInstaller.performInstall(
            downloadURL: downloadURL,
            installedAppURL: installedAppURL,
            expectedTeamIdentifier: nil,
            fileSystem: fileSystem,
            processRunner: processRunner,
            downloader: FakeDownloader(),
            relauncher: relauncher
        )

        XCTAssertEqual(fileSystem.moves.count, 2)
        XCTAssertEqual(relauncher.relaunched, installedAppURL)
    }

    func testSuccessfulSwapMovesBackupAndRelaunches() async throws {
        let fileSystem = FakeFileSystem()
        let processRunner = FakeProcessRunner()
        let relauncher = FakeRelauncher()
        let collector = StageCollector()

        try await UpdateInstaller.performInstall(
            downloadURL: downloadURL,
            installedAppURL: installedAppURL,
            expectedTeamIdentifier: "ABCDE12345",
            fileSystem: fileSystem,
            processRunner: processRunner,
            downloader: FakeDownloader(),
            relauncher: relauncher,
            progress: { collector.record($0) }
        )

        XCTAssertEqual(collector.stages, [.downloading, .verifying, .swapping, .relaunching, .succeeded])
        XCTAssertEqual(fileSystem.moves.count, 2)
        XCTAssertEqual(fileSystem.moves[0].0, installedAppURL)
        XCTAssertTrue(fileSystem.moves[0].1.lastPathComponent.hasSuffix(".backup"))
        XCTAssertEqual(fileSystem.moves[1].1, installedAppURL)
        XCTAssertEqual(relauncher.relaunched, installedAppURL)
    }

    func testSwapFailureRollsBackToPreviousApp() async {
        let fileSystem = FakeFileSystem()
        fileSystem.failSecondMove = true
        let processRunner = FakeProcessRunner()

        do {
            try await UpdateInstaller.performInstall(
                downloadURL: downloadURL,
                installedAppURL: installedAppURL,
                expectedTeamIdentifier: nil,
                fileSystem: fileSystem,
                processRunner: processRunner,
                downloader: FakeDownloader()
            )
            XCTFail("expected swapFailed error")
        } catch UpdateInstallError.swapFailed {
            // moves: [installed->backup, extracted->installed (fails), backup->installed (rollback)]
            XCTAssertEqual(fileSystem.moves.count, 3)
            XCTAssertEqual(fileSystem.moves[2].1, installedAppURL)
        } catch {
            XCTFail("unexpected error: \(error)")
        }
    }

    func testDownloadFailureAbortsBeforeExtraction() async {
        let processRunner = FakeProcessRunner()
        let downloader = FakeDownloader(shouldFail: true)

        do {
            try await UpdateInstaller.performInstall(
                downloadURL: downloadURL,
                installedAppURL: installedAppURL,
                expectedTeamIdentifier: nil,
                processRunner: processRunner,
                downloader: downloader
            )
            XCTFail("expected downloadFailed error")
        } catch UpdateInstallError.downloadFailed {
            XCTAssertTrue(processRunner.calls.isEmpty, "must not attempt extraction after a failed download")
        } catch {
            XCTFail("unexpected error: \(error)")
        }
    }

    func testExtractionFailureAborts() async {
        let processRunner = FakeProcessRunner()
        processRunner.dittoStatus = 1

        do {
            try await UpdateInstaller.performInstall(
                downloadURL: downloadURL,
                installedAppURL: installedAppURL,
                expectedTeamIdentifier: nil,
                processRunner: processRunner,
                downloader: FakeDownloader()
            )
            XCTFail("expected extractionFailed error")
        } catch UpdateInstallError.extractionFailed {
            // expected
        } catch {
            XCTFail("unexpected error: \(error)")
        }
    }

    func testParseTeamIdentifierHandlesNotSet() {
        XCTAssertNil(UpdateInstaller.parseTeamIdentifier(from: "TeamIdentifier=not set"))
        XCTAssertEqual(UpdateInstaller.parseTeamIdentifier(from: "Executable=/x\nTeamIdentifier=ABCDE12345\n"), "ABCDE12345")
        XCTAssertNil(UpdateInstaller.parseTeamIdentifier(from: "no such line"))
    }
}
