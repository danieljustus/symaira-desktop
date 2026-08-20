import Foundation
import XCTest

@testable import SymMeetCore

final class CLITests: XCTestCase {
  private var root: URL!
  private var environment: [String: String]!

  override func setUpWithError() throws {
    root = FileManager.default.temporaryDirectory.appending(
      path: "symmeet-cli-\(UUID().uuidString)")
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    environment = [
      "XDG_CONFIG_HOME": root.appending(path: "config").path,
      "XDG_CACHE_HOME": root.appending(path: "cache").path,
      "XDG_DATA_HOME": root.appending(path: "data").path,
      "SYMMEET_VERSION": "0.1.0-test",
    ]
  }

  override func tearDownWithError() throws {
    try? FileManager.default.removeItem(at: root)
  }

  func testVersionJSONIsTheExactHandshakeWithNoStderr() throws {
    let result = try runCLI(["version", "--json"])

    XCTAssertEqual(result.status, 0)
    XCTAssertEqual(
      result.stdout, "{\"tool\":\"symmeet\",\"version\":\"0.1.0-test\",\"schema_version\":1}\n")
    XCTAssertEqual(result.stderr, "")
  }

  func testDoctorAndConfigJSONRemainMachineReadableWhenDirectoriesAreMissing() throws {
    let firstDoctor = try runCLI(["doctor", "--json"])
    let secondDoctor = try runCLI(["doctor", "--json"])
    let config = try runCLI(["config", "path", "--json"])

    XCTAssertEqual(firstDoctor.status, 0)
    XCTAssertEqual(firstDoctor.stderr, "")
    XCTAssertEqual(firstDoctor.stdout, secondDoctor.stdout)
    let doctor = try XCTUnwrap(jsonObject(firstDoctor.stdout) as? [String: Any])
    XCTAssertNotNil(doctor["architecture"])
    XCTAssertNotNil(doctor["disk"])
    XCTAssertNotNil(doctor["paths"])
    XCTAssertNotNil(doctor["checks"])

    XCTAssertEqual(config.status, 0)
    XCTAssertEqual(config.stderr, "")
    let configJSON = try XCTUnwrap(jsonObject(config.stdout) as? [String: Any])
    XCTAssertEqual(
      configJSON["config_path"] as? String, root.appending(path: "config/symmeet/config.toml").path)
  }

  func testMeetingCommandsKeepStdoutAndStderrSeparate() async throws {
    let dataRoot = root.appending(path: "data/symmeet")
    let manifest = MeetingManifest(
      meetingID: UUID(),
      source: .imported,
      createdAt: Date(timeIntervalSince1970: 0),
      updatedAt: Date(timeIntervalSince1970: 0),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep)
    )
    try await MeetingStore(dataRoot: dataRoot).create(manifest)
    let meetingID = manifest.meetingID.uuidString.lowercased()

    let list = try runCLI(["meeting", "list", "--json"])
    let show = try runCLI(["meeting", "show", meetingID, "--json"])
    let trash = try runCLI(["meeting", "trash", meetingID, "--json"])
    let restore = try runCLI(["meeting", "restore", meetingID, "--json"])

    XCTAssertEqual(list.status, 0)
    XCTAssertEqual(list.stderr, "")
    let listJSON = try XCTUnwrap(jsonObject(list.stdout) as? [String: Any])
    XCTAssertEqual((listJSON["meetings"] as? [[String: Any]])?.count, 1)

    XCTAssertEqual(show.status, 0)
    XCTAssertEqual(show.stderr, "")
    XCTAssertEqual(
      (try XCTUnwrap(jsonObject(show.stdout) as? [String: Any]))["meeting_id"] as? String,
      meetingID.uppercased())

    XCTAssertEqual(trash.status, 0)
    XCTAssertEqual(trash.stderr, "")
    XCTAssertEqual(
      (try XCTUnwrap(jsonObject(trash.stdout) as? [String: Any]))["status"] as? String, "trashed")

    XCTAssertEqual(restore.status, 0)
    XCTAssertEqual(restore.stderr, "")
    XCTAssertEqual(
      (try XCTUnwrap(jsonObject(restore.stdout) as? [String: Any]))["status"] as? String, "restored"
    )
  }

  func testExitCodesForUsageRuntimeAndUnsupportedErrors() throws {
    let unknown = try runCLI(["not-a-command"])
    let missing = try runCLI(["meeting", "show", UUID().uuidString, "--json"])
    let invalidID = try runCLI(["meeting", "trash", "not-a-uuid"])
    let unsupported = try runCLI(["completion", "powershell"])

    XCTAssertEqual(unknown.status, 2)
    XCTAssertEqual(unknown.stdout, "")
    XCTAssertFalse(unknown.stderr.isEmpty)

    XCTAssertEqual(missing.status, 1)
    // #89: --json errors now emit a JSON error document to stdout
    XCTAssertFalse(missing.stdout.isEmpty, "JSON error document should be on stdout in --json mode")
    XCTAssertTrue(missing.stdout.contains("\"error\""), "stdout should contain error envelope")
    XCTAssertEqual(missing.stderr, "", "stderr should be clean in --json mode")

    XCTAssertEqual(invalidID.status, 2)
    XCTAssertEqual(invalidID.stdout, "")
    XCTAssertFalse(invalidID.stderr.isEmpty)

    XCTAssertEqual(unsupported.status, 4)
    XCTAssertEqual(unsupported.stdout, "")
    XCTAssertFalse(unsupported.stderr.isEmpty)
  }

  func testProcessJSONInvalidSpeakersEmitsErrorEnvelope() async throws {
    let dataRoot = root.appending(path: "data/symmeet")
    let manifest = MeetingManifest(
      meetingID: UUID(),
      source: .imported,
      createdAt: Date(timeIntervalSince1970: 0),
      updatedAt: Date(timeIntervalSince1970: 0),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep)
    )
    try await MeetingStore(dataRoot: dataRoot).create(manifest)
    let meetingID = manifest.meetingID.uuidString.lowercased()

    let result = try runCLI(["process", meetingID, "--json", "--speakers", "bogus"])

    XCTAssertEqual(result.status, 2, "stderr: \(result.stderr)")
    XCTAssertEqual(result.stderr, "", "stderr must be clean in --json mode")
    let envelope = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    let error = try XCTUnwrap(envelope["error"] as? [String: Any])
    XCTAssertEqual(error["code"] as? String, "invalid_speakers")
    XCTAssertTrue(
      (error["message"] as? String)?.contains("--speakers") ?? false,
      "envelope message should mention --speakers: \(result.stdout)")
  }

  func testGeneratedCompletionIsAvailableForSupportedShell() throws {
    let result = try runCLI(["completion", "zsh"])

    XCTAssertEqual(result.status, 0)
    XCTAssertTrue(result.stdout.contains("#compdef symmeet"))
    XCTAssertEqual(result.stderr, "")
  }

  func testVersionCheckWithJSONReturnsUpToDateForDevVersion() throws {
    // Dev version "0.1.0-test" is rejected by StableVersion (prerelease suffix),
    // so UpdateChecker returns nil = up-to-date without hitting the network.
    let result = try runCLI(["version", "check", "--output-json"])

    XCTAssertEqual(result.status, 0, "stderr: \(result.stderr)")
    let json = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    XCTAssertEqual(json["current_version"] as? String, "0.1.0-test")
    XCTAssertNil(json["new_version"])
    XCTAssertNil(json["url"])
    XCTAssertNil(json["error"])
  }

  func testRecordWithoutFreshAttestationExits3WithConsentMessage() throws {
    // Running record without --yes and with stdin closed should result in
    // a consent-refusal error (exit code 3 / permissionDenied).
    let result = try runCLI(["record", "--purpose", "test-attestation"], closeStdin: true)
    XCTAssertEqual(result.status, 3, "stderr: \(result.stderr)")
    XCTAssertTrue(
      result.stderr.localizedCaseInsensitiveContains("authorization")
        || result.stderr.localizedCaseInsensitiveContains("attestation")
        || result.stderr.localizedCaseInsensitiveContains("consent"),
      "Expected a consent/permission message but got: \(result.stderr)")
    XCTAssertEqual(result.stdout, "")
  }

  private func runCLI(_ arguments: [String], closeStdin: Bool = false) throws -> CLIResult {
    let binary = URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
      .appending(path: ".build/debug/symmeet")
    XCTAssertTrue(FileManager.default.isExecutableFile(atPath: binary.path))

    let process = Process()
    let stdout = Pipe()
    let stderr = Pipe()
    process.executableURL = binary
    process.arguments = arguments
    process.environment = ProcessInfo.processInfo.environment.merging(
      environment, uniquingKeysWith: { _, new in new })
    process.standardOutput = stdout
    process.standardError = stderr
    if closeStdin {
      let stdinPipe = Pipe()
      stdinPipe.fileHandleForWriting.closeFile()
      process.standardInput = stdinPipe
    }
    try process.run()
    process.waitUntilExit()

    return CLIResult(
      status: process.terminationStatus,
      stdout: String(decoding: stdout.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self),
      stderr: String(decoding: stderr.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
    )
  }

  private func jsonObject(_ text: String) throws -> Any {
    try JSONSerialization.jsonObject(with: Data(text.utf8))
  }

  func testDoctorHumanOutputIncludesActionableRemediation() throws {
    let result = try runCLI(["doctor"])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertEqual(result.stderr, "")
    XCTAssertTrue(result.stdout.contains("Artifact store: warn"), result.stdout)
    XCTAssertTrue(result.stdout.contains("Capture: warn"), result.stdout)
    XCTAssertTrue(result.stdout.contains("Models: warn"), result.stdout)
    XCTAssertTrue(result.stdout.contains("Remediation:"), result.stdout)
  }

  func testDoctorJSONUsesTypedStatusesAndRemediation() throws {
    let result = try runCLI(["doctor", "--json"])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertEqual(result.stderr, "")
    let doctor = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    let checks = try XCTUnwrap(doctor["checks"] as? [String: Any])

    for name in ["artifact_store", "capture", "models"] {
      let check = try XCTUnwrap(checks[name] as? [String: Any])
      XCTAssertEqual(check["status"] as? String, "warn")
      XCTAssertNotNil(check["remediation"] as? String)
    }
  }

  func testDoctorJSONExitsWithFailureWhenArtifactStoreIsUnwritable() throws {
    let blockedDataHome = root.appending(path: "blocked-data-home")
    let dataDirectory = blockedDataHome.appending(path: "symmeet")
    try FileManager.default.createDirectory(at: dataDirectory, withIntermediateDirectories: true)
    try FileManager.default.setAttributes(
      [.posixPermissions: 0o444], ofItemAtPath: dataDirectory.path)
    environment["XDG_DATA_HOME"] = blockedDataHome.path

    let result = try runCLI(["doctor", "--json"])

    XCTAssertEqual(result.status, 1, result.stderr)
    XCTAssertEqual(result.stderr, "")
    let doctor = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    let checks = try XCTUnwrap(doctor["checks"] as? [String: Any])
    let artifactStore = try XCTUnwrap(checks["artifact_store"] as? [String: Any])
    XCTAssertEqual(artifactStore["status"] as? String, "fail")
    XCTAssertNotNil(artifactStore["remediation"] as? String)
  }
}

private struct CLIResult {
  let status: Int32
  let stdout: String
  let stderr: String
}
