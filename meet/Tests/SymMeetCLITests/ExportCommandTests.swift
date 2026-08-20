import Foundation
import XCTest

@testable import SymMeetCore

final class ExportCommandTests: XCTestCase {
  private var root: URL!
  private var environment: [String: String]!
  private var dataRoot: URL!

  override func setUpWithError() throws {
    root = FileManager.default.temporaryDirectory.appending(
      path: "symmeet-export-\(UUID().uuidString)")
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    dataRoot = root.appending(path: "data/symmeet")
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

  // MARK: - Happy path: real content, not just exit code 0

  func testExportRendersRealMeetingContentToFile() async throws {
    let meetingID = try await makeCompletedMeeting()
    let output = root.appending(path: "transcript.md")

    let result = try runCLI(["export", meetingID, "--format", "markdown", "--output", output.path])

    XCTAssertEqual(result.status, 0, result.stderr)
    let content = try String(contentsOf: output, encoding: .utf8)
    XCTAssertTrue(content.contains("## Transcript"))
    XCTAssertTrue(content.contains("speaker_1"))
    XCTAssertTrue(content.contains("Hello from the export test."))
  }

  // MARK: - Overwrite protection (acceptance criterion #5)

  func testExportRefusesToOverwriteExistingFileWithoutForce() async throws {
    let meetingID = try await makeCompletedMeeting()
    let output = root.appending(path: "transcript.txt")
    try Data("placeholder".utf8).write(to: output)

    let blocked = try runCLI(["export", meetingID, "--format", "txt", "--output", output.path])
    XCTAssertNotEqual(blocked.status, 0)
    XCTAssertTrue(blocked.stderr.contains("--force"), blocked.stderr)
    XCTAssertEqual(blocked.stdout, "")
    XCTAssertEqual(try String(contentsOf: output, encoding: .utf8), "placeholder")

    let forced = try runCLI(
      ["export", meetingID, "--format", "txt", "--output", output.path, "--force"])
    XCTAssertEqual(forced.status, 0, forced.stderr)
    let content = try String(contentsOf: output, encoding: .utf8)
    XCTAssertTrue(content.contains("Hello from the export test."))
  }

  // MARK: - stdout/stderr separation (acceptance criterion #6)

  func testExportToStdoutWritesOnlyContentNoStderrNoise() async throws {
    let meetingID = try await makeCompletedMeeting()

    let result = try runCLI(["export", meetingID, "--format", "jsonl", "--output", "-"])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertEqual(result.stderr, "")
    XCTAssertTrue(result.stdout.contains("Hello from the export test."))
    // Only the rendered content -- no confirmation banner, no progress text.
    XCTAssertFalse(result.stdout.contains("Exported"))
  }

  func testJSONFlagIsRejectedWithStdoutOutput() async throws {
    let meetingID = try await makeCompletedMeeting()

    let result = try runCLI(["export", meetingID, "--format", "json", "--output", "-", "--json"])

    XCTAssertEqual(result.status, 2)
    XCTAssertEqual(result.stderr, "", "stderr must be clean in --json mode")
    let envelope = try XCTUnwrap(jsonObject(result.stdout) as? [String: Any])
    let error = try XCTUnwrap(envelope["error"] as? [String: Any])
    XCTAssertEqual(error["code"] as? String, "export_failed")
  }

  // MARK: - Reproducibility (rules: byte-identical re-export)

  func testExportTwiceProducesByteIdenticalOutput() async throws {
    let meetingID = try await makeCompletedMeeting()
    let firstOutput = root.appending(path: "first.srt")
    let secondOutput = root.appending(path: "second.srt")

    let first = try runCLI(["export", meetingID, "--format", "srt", "--output", firstOutput.path])
    let second = try runCLI(
      ["export", meetingID, "--format", "srt", "--output", secondOutput.path])

    XCTAssertEqual(first.status, 0, first.stderr)
    XCTAssertEqual(second.status, 0, second.stderr)
    let firstData = try Data(contentsOf: firstOutput)
    let secondData = try Data(contentsOf: secondOutput)
    XCTAssertEqual(firstData, secondData)
  }

  // MARK: - Trashed / incomplete overrides (acceptance criterion #7)

  func testExportRequiresAllowIncompleteForUnfinishedJob() async throws {
    let store = MeetingStore(dataRoot: dataRoot)
    let meetingID = UUID()
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(timeIntervalSince1970: 0),
      updatedAt: Date(timeIntervalSince1970: 0),
      job: MeetingJob(jobID: UUID(), state: .processing),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)
    let idString = meetingID.uuidString.lowercased()

    let blocked = try runCLI(["export", idString, "--format", "txt", "--output", "-"])
    XCTAssertNotEqual(blocked.status, 0)
    XCTAssertTrue(blocked.stderr.contains("--allow-incomplete"), blocked.stderr)
    XCTAssertEqual(blocked.stdout, "")

    let allowed = try runCLI(
      ["export", idString, "--format", "txt", "--output", "-", "--allow-incomplete"])
    XCTAssertEqual(allowed.status, 0, allowed.stderr)
  }

  func testExportRequiresIncludeTrashedForTrashedMeeting() async throws {
    let meetingID = try await makeCompletedMeeting()
    let store = MeetingStore(dataRoot: dataRoot)
    try await store.trash(meetingID: meetingID)

    let blocked = try runCLI(["export", meetingID, "--format", "txt", "--output", "-"])
    XCTAssertNotEqual(blocked.status, 0)
    XCTAssertTrue(blocked.stderr.contains("trash"), blocked.stderr)
    XCTAssertEqual(blocked.stdout, "")

    let allowed = try runCLI(
      ["export", meetingID, "--format", "txt", "--output", "-", "--include-trashed"])
    XCTAssertEqual(allowed.status, 0, allowed.stderr)
    XCTAssertTrue(allowed.stdout.contains("Hello from the export test."))
  }

  func testExportOfNonexistentMeetingFailsClearly() throws {
    let result = try runCLI(["export", UUID().uuidString, "--format", "txt", "--output", "-"])
    XCTAssertNotEqual(result.status, 0)
    XCTAssertEqual(result.stdout, "")
    XCTAssertFalse(result.stderr.isEmpty)
  }

  // MARK: - Argument validation

  func testExportRejectsUnknownFormat() async throws {
    let meetingID = try await makeCompletedMeeting()
    let result = try runCLI(["export", meetingID, "--format", "pdf", "--output", "-"])
    XCTAssertEqual(result.status, 2)
    XCTAssertTrue(result.stderr.contains("Unknown format"), result.stderr)
  }

  func testExportRejectsUnknownSegmentSource() async throws {
    let meetingID = try await makeCompletedMeeting()
    let result = try runCLI(
      ["export", meetingID, "--format", "txt", "--output", "-", "--segments", "final"])
    XCTAssertEqual(result.status, 2)
    XCTAssertTrue(result.stderr.contains("Unknown segment source"), result.stderr)
  }

  // MARK: - Segment source selection

  func testExportPrefersEditedSegmentsWhenPresentOnDisk() async throws {
    let meetingID = try await makeCompletedMeeting()
    try writeEditedSegments(forMeetingID: meetingID)

    let result = try runCLI(["export", meetingID, "--format", "txt", "--output", "-"])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertTrue(result.stdout.contains("This is the edited version."))
    XCTAssertFalse(result.stdout.contains("Hello from the export test."))
  }

  func testExportSegmentsRawIgnoresEditedOverlay() async throws {
    let meetingID = try await makeCompletedMeeting()
    try writeEditedSegments(forMeetingID: meetingID)

    let result = try runCLI(
      ["export", meetingID, "--format", "txt", "--output", "-", "--segments", "raw"])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertTrue(result.stdout.contains("Hello from the export test."))
  }

  func testExportSegmentsEditedFailsExplicitlyWhenUnavailable() async throws {
    let meetingID = try await makeCompletedMeeting()

    let result = try runCLI(
      ["export", meetingID, "--format", "txt", "--output", "-", "--segments", "edited"])

    XCTAssertNotEqual(result.status, 0)
    XCTAssertTrue(result.stderr.contains("No edited segments"), result.stderr)
  }

  // MARK: - Help text

  func testExportAppearsInTopLevelHelp() throws {
    let result = try runCLI(["--help"])
    XCTAssertEqual(result.status, 0)
    XCTAssertTrue(result.stdout.contains("export"))
  }

  func testExportHelpDocumentsFormatsAndFlags() throws {
    let result = try runCLI(["export", "--help"])
    XCTAssertEqual(result.status, 0)
    XCTAssertTrue(result.stdout.contains("markdown"))
    XCTAssertTrue(result.stdout.contains("--with-timestamps"))
    XCTAssertTrue(result.stdout.contains("--force"))
  }

  // MARK: - Helpers

  @discardableResult
  private func makeCompletedMeeting() async throws -> String {
    let store = MeetingStore(dataRoot: dataRoot)
    let meetingID = UUID()
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(timeIntervalSince1970: 0),
      updatedAt: Date(timeIntervalSince1970: 0),
      language: "en",
      job: MeetingJob(
        jobID: UUID(), state: .completed,
        engine: EngineProvenance(engineID: "test-engine", modelID: "tiny", modelVersion: "v1")),
      consent: ConsentState(status: .authorized),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)

    let trackID = UUID()
    let segment = try Segment(
      segmentID: UUID(), trackID: trackID, speakerID: "speaker_1",
      startMS: 0, endMS: 1_500, engineText: "Hello from the export test.")
    try await store.appendRawSegment(segment, meetingID: meetingID.uuidString)

    return meetingID.uuidString.lowercased()
  }

  /// Writes directly to `segments.edited.jsonl` -- there is no writer for
  /// this file anywhere else in the codebase yet, so tests exercising the
  /// "prefer edited segments" rule construct the overlay by hand.
  private func writeEditedSegments(forMeetingID meetingID: String) throws {
    let layout = ArtifactLayout(dataRoot: dataRoot)
    let directory = layout.meetingDirectory(meetingID)
    let trackID = UUID()
    let segment = try Segment(
      segmentID: UUID(), trackID: trackID, speakerID: "speaker_1",
      startMS: 0, endMS: 1_500, engineText: "This is the edited version.")
    let data = try ContractCodec.encoder().encode(segment)
    var line = data
    line.append(0x0A)
    try line.write(to: layout.editedSegmentsURL(in: directory))
  }

  private func jsonObject(_ text: String) throws -> Any {
    try JSONSerialization.jsonObject(with: Data(text.utf8))
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
}

private struct CLIResult {
  let status: Int32
  let stdout: String
  let stderr: String
}
