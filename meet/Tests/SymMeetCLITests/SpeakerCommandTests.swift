import Foundation
import XCTest

@testable import SymMeetCore

final class SpeakerCommandTests: XCTestCase {
  private var root: URL!
  private var environment: [String: String]!
  private var dataRoot: URL!

  override func setUpWithError() throws {
    root = FileManager.default.temporaryDirectory.appending(
      path: "symmeet-speaker-\(UUID().uuidString)")
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

  // MARK: - Speaker list

  func testSpeakerListShowsNoSpeakersForEmptyMeeting() async throws {
    let meetingID = try await makeMeetingWithTurns([])

    let result = try runCLI(["speaker", "list", meetingID])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertEqual(result.stderr, "")
    XCTAssertTrue(result.stdout.contains("No speakers found"))
  }

  func testSpeakerListShowsSpeakers() async throws {
    let meetingID = try await makeMeetingWithTurns([
      "speaker_0", "speaker_1", "speaker_0",
    ])

    let result = try runCLI(["speaker", "list", meetingID])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertTrue(result.stdout.contains("speaker_0"))
    XCTAssertTrue(result.stdout.contains("speaker_1"))
  }

  func testSpeakerListJSONIsMachineReadable() async throws {
    let meetingID = try await makeMeetingWithTurns(["speaker_0"])

    let result = try runCLI(["speaker", "list", meetingID, "--json"])

    XCTAssertEqual(result.status, 0, result.stderr)
    let json = try jsonObject(result.stdout) as? [String: Any]
    XCTAssertEqual(json?["meeting_id"] as? String, meetingID)
    let speakers = json?["speakers"] as? [String]
    XCTAssertEqual(speakers, ["speaker_0"])
  }

  // MARK: - Speaker label

  func testSpeakerLabelAssignsLabel() async throws {
    let meetingID = try await makeMeetingWithTurns(["speaker_0"])

    let result = try runCLI(
      ["speaker", "label", meetingID, "speaker_0", "Alice"])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertTrue(result.stdout.contains("Labeled"))
    XCTAssertTrue(result.stdout.contains("Alice"))

    let list = try runCLI(["speaker", "list", meetingID, "--json"])
    let json = try jsonObject(list.stdout) as? [String: Any]
    let labels = json?["labels"] as? [String: String]
    XCTAssertEqual(labels?["speaker_0"], "Alice")
  }

  func testSpeakerLabelRejectsUnknownSpeaker() async throws {
    let meetingID = try await makeMeetingWithTurns(["speaker_0"])

    let result = try runCLI(
      ["speaker", "label", meetingID, "speaker_99", "Ghost"])

    XCTAssertNotEqual(result.status, 0)
    XCTAssertEqual(result.stdout, "")
  }

  func testSpeakerLabelUnicodeLabel() async throws {
    let meetingID = try await makeMeetingWithTurns(["speaker_0"])

    let result = try runCLI(
      ["speaker", "label", meetingID, "speaker_0", "テスト発言者"])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertTrue(result.stdout.contains("テスト発言者"))
  }

  // MARK: - Speaker merge

  func testSpeakerMergeCombinesSpeakers() async throws {
    let meetingID = try await makeMeetingWithTurns([
      "speaker_0", "speaker_1", "speaker_2",
    ])

    let result = try runCLI(
      ["speaker", "merge", meetingID, "speaker_1", "speaker_0"])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertTrue(result.stdout.contains("Merged"))

    let list = try runCLI(["speaker", "list", meetingID, "--json"])
    let json = try jsonObject(list.stdout) as? [String: Any]
    let merged = json?["merged_speakers"] as? [String: [String]]
    XCTAssertEqual(merged?["speaker_0"], ["speaker_1"])
  }

  func testSpeakerMergeRejectsMergeIntoSelf() async throws {
    let meetingID = try await makeMeetingWithTurns(["speaker_0"])

    let result = try runCLI(
      ["speaker", "merge", meetingID, "speaker_0", "speaker_0"])

    XCTAssertNotEqual(result.status, 0)
    XCTAssertEqual(result.stdout, "")
  }

  func testSpeakerMergeRejectsUnknownSpeaker() async throws {
    let meetingID = try await makeMeetingWithTurns(["speaker_0"])

    let result = try runCLI(
      ["speaker", "merge", meetingID, "speaker_99", "speaker_0"])

    XCTAssertNotEqual(result.status, 0)
    XCTAssertEqual(result.stdout, "")
  }

  // MARK: - Speaker split

  func testSpeakerSplitSeparatesSegment() async throws {
    let meetingID = try await makeMeetingWithTurns(["speaker_0", "speaker_1"])
    let segments = try await loadSegments(meetingID: meetingID)
    let segmentID = segments.first?.segmentID.uuidString ?? ""

    let result = try runCLI(
      ["speaker", "split", meetingID, "speaker_0", "--segment", segmentID])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertTrue(result.stdout.contains("Split"))
  }

  func testSpeakerSplitRejectsUnknownSegment() async throws {
    let meetingID = try await makeMeetingWithTurns(["speaker_0"])
    let fakeSegmentID = UUID().uuidString

    let result = try runCLI(
      ["speaker", "split", meetingID, "speaker_0", "--segment", fakeSegmentID])

    XCTAssertNotEqual(result.status, 0)
  }

  // MARK: - Speaker reset

  func testSpeakerResetClearsAllEdits() async throws {
    let meetingID = try await makeMeetingWithTurns(["speaker_0", "speaker_1"])
    _ = try runCLI(
      ["speaker", "label", meetingID, "speaker_0", "Alice"])
    _ = try runCLI(
      ["speaker", "merge", meetingID, "speaker_1", "speaker_0"])

    let result = try runCLI(["speaker", "reset", meetingID])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertTrue(result.stdout.contains("Reset"))

    let list = try runCLI(["speaker", "list", meetingID, "--json"])
    let json = try jsonObject(list.stdout) as? [String: Any]
    let labels = json?["labels"] as? [String: String]
    XCTAssertTrue(labels?.isEmpty ?? true)
  }

  // MARK: - Overlapping speakers

  func testOverlappingSpeakersListed() async throws {
    let meetingID = try await makeMeetingWithTurns([
      "speaker_0", "speaker_1", "speaker_0", "speaker_1",
    ])

    let result = try runCLI(["speaker", "list", meetingID])

    XCTAssertEqual(result.status, 0, result.stderr)
    XCTAssertTrue(result.stdout.contains("speaker_0"))
    XCTAssertTrue(result.stdout.contains("speaker_1"))
  }

  // MARK: - Help

  func testSpeakerAppearsInTopLevelHelp() throws {
    let result = try runCLI(["--help"])
    XCTAssertEqual(result.status, 0)
    XCTAssertTrue(result.stdout.contains("speaker"))
  }

  func testSpeakerHelpDocumentsSubcommands() throws {
    let result = try runCLI(["speaker", "--help"])
    XCTAssertEqual(result.status, 0)
    XCTAssertTrue(result.stdout.contains("list"))
    XCTAssertTrue(result.stdout.contains("label"))
    XCTAssertTrue(result.stdout.contains("merge"))
    XCTAssertTrue(result.stdout.contains("split"))
    XCTAssertTrue(result.stdout.contains("reset"))
  }

  // MARK: - Nonexistent meeting

  func testSpeakerListNonexistentMeetingFails() throws {
    let result = try runCLI(["speaker", "list", UUID().uuidString])
    XCTAssertNotEqual(result.status, 0)
    XCTAssertEqual(result.stdout, "")
  }

  // MARK: - JSON contract shape

  func testSpeakerListJSONContractShape() async throws {
    let meetingID = try await makeMeetingWithTurns(["speaker_0", "speaker_1"])

    let result = try runCLI(["speaker", "list", meetingID, "--json"])

    XCTAssertEqual(result.status, 0, result.stderr)
    let json = try jsonObject(result.stdout) as? [String: Any]
    XCTAssertNotNil(json?["meeting_id"])
    XCTAssertNotNil(json?["speakers"])
    XCTAssertNotNil(json?["labels"])
    XCTAssertNotNil(json?["merged_speakers"])
  }

  // MARK: - Helpers

  @discardableResult
  private func makeMeetingWithTurns(_ speakerIDs: [String]) async throws -> String {
    let store = MeetingStore(dataRoot: dataRoot)
    let meetingID = UUID()
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(timeIntervalSince1970: 0),
      updatedAt: Date(timeIntervalSince1970: 0),
      language: "en",
      job: MeetingJob(jobID: UUID(), state: .completed),
      consent: ConsentState(status: .authorized),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)
    let normalizedID = meetingID.uuidString.lowercased()

    let trackID = UUID()
    var offset = 0
    for speakerID in speakerIDs {
      let segment = try Segment(
        segmentID: UUID(), trackID: trackID, speakerID: speakerID,
        startMS: offset, endMS: offset + 1_000,
        engineText: "Turn from \(speakerID)")
      try await store.appendRawSegment(segment, meetingID: normalizedID)
      let turn = try SpeakerTurn(
        speakerID: speakerID, startMS: offset, endMS: offset + 1_000)
      try await store.appendRawTurns([turn], meetingID: normalizedID)
      offset += 1_000
    }

    return normalizedID
  }

  private func loadSegments(meetingID: String) async throws -> [Segment] {
    let store = MeetingStore(dataRoot: dataRoot)
    return try await store.rawSegments(meetingID: meetingID)
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
