import XCTest
@testable import SymDeskCore
import SymairaCLIRunner
import SymairaToolKit

/// Argument construction for the export path (issue #514). The profile is the
/// only thing that decides which layout `print/` renders, so the app dropping
/// it is invisible in the output — a test on the arguments is what catches it.
@MainActor
final class DeskCoreExportTests: XCTestCase {
    private var previousTransport: DeskTransport?
    private var previousVaultPath: String?
    private var transport: RecordingTransport!

    override func setUp() async throws {
        try await super.setUp()
        previousTransport = DeskCore.shared.transport
        previousVaultPath = DeskCore.shared.vaultPath
        transport = RecordingTransport()
        DeskCore.shared.transport = transport
        DeskCore.shared.vaultPath = "/tmp/vault"
    }

    override func tearDown() async throws {
        DeskCore.shared.transport = previousTransport
        DeskCore.shared.vaultPath = previousVaultPath
        transport = nil
        try await super.tearDown()
    }

    func testExportNoteOmitsProfileFlagWhenNoProfileIsChosen() async throws {
        transport.response = Data(#"{"format":"pdf","path":"/tmp/out.pdf","rendered":true}"#.utf8)

        _ = try await DeskCore.shared.exportNote(path: "notes/a.md", format: "pdf", outputPath: "/tmp/out.pdf")

        let arguments = transport.lastArguments
        XCTAssertFalse(arguments.contains("--profile"))
        XCTAssertEqual(
            arguments,
            ["export", "--note", "notes/a.md", "--format", "pdf", "--output", "/tmp/out.pdf", "--json", "--vault", "/tmp/vault"]
        )
    }

    func testExportNoteForwardsChosenProfile() async throws {
        transport.response = Data(#"{"format":"pdf","path":"/tmp/out.pdf","profile":"report","rendered":true}"#.utf8)

        let result = try await DeskCore.shared.exportNote(
            path: "notes/a.md", format: "pdf", outputPath: "/tmp/out.pdf", profile: "report"
        )

        XCTAssertEqual(result.profile, "report")
        let arguments = transport.lastArguments
        guard let index = arguments.firstIndex(of: "--profile") else {
            return XCTFail("expected --profile in \(arguments)")
        }
        XCTAssertEqual(arguments[arguments.index(after: index)], "report")
    }

    /// The profile list is a property of the renderer, not of a vault, so the
    /// command must not carry `--vault` — the CLI subcommand opens no vault.
    func testExportProfilesQueriesTheCoreWithoutAVault() async throws {
        transport.response = Data(#"[{"name":"report","title":"Report","description":"d","stability":"beta"}]"#.utf8)

        let profiles = try await DeskCore.shared.exportProfiles()

        XCTAssertEqual(profiles.map(\.name), ["report"])
        XCTAssertEqual(transport.lastArguments, ["export", "profiles", "--json"])
    }
}

/// Records what the core asked the transport to run and replays a canned
/// response.
private final class RecordingTransport: DeskTransport, @unchecked Sendable {
    var response = Data("{}".utf8)
    private(set) var lastArguments: [String] = []

    func command(arguments: [String], stdin: String) async throws -> Data {
        lastArguments = arguments
        return response
    }

    func commandResult(arguments: [String]) async throws -> CLIResult {
        lastArguments = arguments
        return CLIResult(stdout: response, stderr: Data(), exitCode: 0)
    }

    func commandStream(arguments: [String], stdin: String) -> AsyncThrowingStream<String, Error> {
        lastArguments = arguments
        return AsyncThrowingStream { $0.finish() }
    }

    func fileContent(path: String) async throws -> String { "" }
    func saveFile(path: String, content: String) async throws {}
    func ingestFile(_ fileURL: URL, vaultArgs: [String]) async throws -> String { "" }
    func ingestJobs(vaultArgs: [String]) async throws -> [IngestJob] { [] }
    func ingestRetry(jobID: String, vaultArgs: [String]) async throws {}
}
