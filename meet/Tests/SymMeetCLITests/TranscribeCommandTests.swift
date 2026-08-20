import Foundation
import XCTest

@testable import SymMeetCore

final class TranscribeCommandTests: XCTestCase {
  private var root: URL!
  private var environment: [String: String]!

  override func setUpWithError() throws {
    root = FileManager.default.temporaryDirectory.appending(
      path: "symmeet-transcribe-\(UUID().uuidString)")
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

  func testTranscribeShowsInHelp() throws {
    let result = try runCLI(["--help"])
    XCTAssertEqual(result.status, 0)
    XCTAssertTrue(result.stdout.contains("transcribe"), "transcribe must appear in help")
    XCTAssertTrue(result.stdout.contains("job"), "job must appear in help")
  }

  func testTranscribeMissingFileEmitsJSONErrorEnvelope() throws {
    let result = try runCLI(["transcribe", "/nonexistent/audio.wav", "--json"])
    XCTAssertEqual(result.status, 2)
    XCTAssertEqual(result.stderr, "", "stderr must be clean in --json mode")
    let envelope = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    let error = try XCTUnwrap(envelope["error"] as? [String: Any])
    XCTAssertEqual(error["code"] as? String, "file_not_found")
    XCTAssertTrue(
      (error["message"] as? String)?.contains("File not found") ?? false,
      "envelope message should mention the missing file: \(result.stdout)")
  }

  func testTranscribeMissingModelReportsInstallCommand() throws {
    // "large-v3-v20240930_626MB" is a real catalog entry that is never
    // installed in this test's isolated XDG roots, exercising the
    // modelNotInstalled path (as opposed to an unrecognized catalog ID).
    let modelID = "large-v3-v20240930_626MB"
    let dummy = root.appending(path: "test.wav")
    try Data(count: 100).write(to: dummy)
    let result = try runCLI(["transcribe", dummy.path, "--model", modelID, "--json"])
    XCTAssertEqual(result.status, 1)
    XCTAssertEqual(result.stderr, "", "stderr must be clean in --json mode")
    let envelope = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    let error = try XCTUnwrap(envelope["error"] as? [String: Any])
    XCTAssertEqual(error["code"] as? String, "model_not_installed")
    let message = try XCTUnwrap(error["message"] as? String)
    XCTAssertTrue(
      message.contains("not installed"),
      "must report model not installed: \(message)")
    XCTAssertTrue(
      message.contains("symmeet model install \(modelID)"),
      "must suggest install command: \(message)")
  }

  func testJobListEmptyShowsNoJobs() throws {
    let result = try runCLI(["job", "list"])
    XCTAssertEqual(result.status, 0)
    XCTAssertEqual(result.stdout.trimmingCharacters(in: .whitespacesAndNewlines), "No jobs found.")
    XCTAssertEqual(result.stderr, "")
  }

  func testJobListJSONEmptyShowsEmptyArray() throws {
    let result = try runCLI(["job", "list", "--json"])
    XCTAssertEqual(result.status, 0)
    let json = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    let jobs = json["jobs"] as? [Any]
    XCTAssertEqual(jobs?.count, 0)
    XCTAssertEqual(result.stderr, "")
  }

  func testJobListShowsSeededJob() async throws {
    let dataRoot = root.appending(path: "data/symmeet")
    let coordinator = JobCoordinator(dataRoot: dataRoot)
    let meetingID = UUID()
    let job = try await coordinator.enqueue(meetingID: meetingID)

    let result = try runCLI(["job", "list", "--json"])
    XCTAssertEqual(result.status, 0)
    let json = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    let jobs = json["jobs"] as? [[String: Any]]
    XCTAssertEqual(jobs?.count, 1)
    XCTAssertEqual(
      jobs?.first?["meeting_id"] as? String,
      meetingID.uuidString)
    XCTAssertEqual(result.stderr, "")
  }

  func testJobShowWithMeetingID() async throws {
    let dataRoot = root.appending(path: "data/symmeet")
    let coordinator = JobCoordinator(dataRoot: dataRoot)
    let meetingID = UUID()
    let job = try await coordinator.enqueue(meetingID: meetingID)

    // Lowercase input must still resolve (case-insensitive UUID lookup); the
    // JSON contract itself preserves UUID.uuidString's own casing.
    let id = meetingID.uuidString.lowercased()
    let result = try runCLI(["job", "show", id, "--json"])
    XCTAssertEqual(result.status, 0)
    let json = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    XCTAssertEqual(json["meeting_id"] as? String, meetingID.uuidString)
    XCTAssertEqual(json["status"] as? String, "queued")
    XCTAssertEqual(result.stderr, "")
  }

  func testJobShowWithInvalidIDEmitsJSONErrorEnvelope() throws {
    let result = try runCLI(["job", "show", "not-a-uuid", "--json"])
    XCTAssertEqual(result.status, 2)
    XCTAssertEqual(result.stderr, "", "stderr must be clean in --json mode")
    let envelope = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    let error = try XCTUnwrap(envelope["error"] as? [String: Any])
    XCTAssertEqual(error["code"] as? String, "job_operation_failed")
    XCTAssertTrue(
      (error["message"] as? String)?.contains("No job found") ?? false,
      "envelope message should mention the missing job: \(result.stdout)")
  }

  func testJobShowWithNonexistentMeetingEmitsJSONErrorEnvelope() throws {
    let id = UUID().uuidString.lowercased()
    let result = try runCLI(["job", "show", id, "--json"])
    XCTAssertEqual(result.status, 2)
    XCTAssertEqual(result.stderr, "", "stderr must be clean in --json mode")
    let envelope = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    XCTAssertNotNil(envelope["error"])
  }

  func testJobCancelQueuedJob() async throws {
    let dataRoot = root.appending(path: "data/symmeet")
    let coordinator = JobCoordinator(dataRoot: dataRoot)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)

    let id = meetingID.uuidString.lowercased()
    let result = try runCLI(["job", "cancel", id, "--json"])
    XCTAssertEqual(result.status, 0)
    let json = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    XCTAssertEqual(json["status"] as? String, "cancelled")
    XCTAssertEqual(result.stderr, "")

    let reloaded = try await coordinator.load(meetingID: meetingID)
    XCTAssertEqual(reloaded.status, .cancelled)
  }

  func testJobListFilterByState() async throws {
    let dataRoot = root.appending(path: "data/symmeet")
    let coordinator = JobCoordinator(dataRoot: dataRoot)
    let meeting1 = UUID()
    let meeting2 = UUID()
    _ = try await coordinator.enqueue(meetingID: meeting1)
    _ = try await coordinator.enqueue(meetingID: meeting2)
    let handle = try await coordinator.lock.acquire(meetingID: meeting1)
    _ = try await coordinator.advance(meetingID: meeting1, to: .preparing, using: handle)
    _ = try await coordinator.advance(meetingID: meeting1, to: .transcribing, using: handle)

    let result = try runCLI(["job", "list", "--state", "queued", "--json"])
    XCTAssertEqual(result.status, 0)
    let json = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    let jobs = json["jobs"] as? [[String: Any]]
    XCTAssertEqual(jobs?.count, 1)
    XCTAssertEqual(jobs?.first?["meeting_id"] as? String, meeting2.uuidString)
    XCTAssertEqual(result.stderr, "")
  }

  private func runCLI(_ arguments: [String]) throws -> CLIResult {
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
    try process.run()
    process.waitUntilExit()

    return CLIResult(
      status: process.terminationStatus,
      stdout: String(decoding: stdout.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self),
      stderr: String(
        decoding: stderr.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self))
  }

  private func jsonObject(_ text: String) throws -> Any {
    try JSONSerialization.jsonObject(with: Data(text.utf8))
  }
}

private struct CLIResult {
  let status: Int32
  let stdout: String
  let stderr: String
}
