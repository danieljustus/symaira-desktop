import Foundation
import SymMeetCore
import XCTest

@testable import SymMeetMCP

final class JobToolHandlersTests: XCTestCase {
  private var root: URL!

  override func setUpWithError() throws {
    root = try makeTemporaryDirectory()
  }

  override func tearDownWithError() throws {
    try? FileManager.default.removeItem(at: root)
  }

  // MARK: - meeting_job_status

  func testJobStatusRequiresJobID() async throws {
    let store = MeetingStore(dataRoot: root)
    let handler = MeetingJobStatusHandler(store: store, dataRoot: root)
    let result = try await handler.execute(args: [:])

    XCTAssertTrue(result.isError)
    XCTAssertTrue(
      result.content.first?.text?.contains("Missing required parameter: job_id") ?? false)
  }

  func testJobStatusUnknownIdentifierThrows() async {
    let store = MeetingStore(dataRoot: root)
    let handler = MeetingJobStatusHandler(store: store, dataRoot: root)

    await assertThrowsAsync {
      try await handler.execute(args: ["job_id": AnyCodable(UUID().uuidString)])
    }
  }

  func testJobStatusReturnsSnakeCaseShape() async throws {
    let store = MeetingStore(dataRoot: root)
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    let job = try await coordinator.enqueue(meetingID: meetingID)
    let handler = MeetingJobStatusHandler(store: store, dataRoot: root)

    let result = try await handler.execute(
      args: ["job_id": AnyCodable(job.jobID.uuidString.lowercased())])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(Set(json.keys), Set(["job_id", "meeting_id", "status", "attempt"]))
    XCTAssertEqual(json["job_id"] as? String, job.jobID.uuidString.lowercased())
    XCTAssertEqual(json["meeting_id"] as? String, meetingID.uuidString.lowercased())
    XCTAssertEqual(json["status"] as? String, "queued")
    XCTAssertEqual(json["attempt"] as? Int, 1)
    // No engine provenance on a freshly enqueued job: the key is omitted.
    XCTAssertNil(json["engine"])
  }

  func testJobStatusResolvesByMeetingID() async throws {
    let store = MeetingStore(dataRoot: root)
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)
    let handler = MeetingJobStatusHandler(store: store, dataRoot: root)

    let result = try await handler.execute(
      args: ["job_id": AnyCodable(meetingID.uuidString.lowercased())])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(json["meeting_id"] as? String, meetingID.uuidString.lowercased())
    XCTAssertEqual(json["status"] as? String, "queued")
  }

  func testJobStatusIncludesEngineProvenance() async throws {
    let store = MeetingStore(dataRoot: root)
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    _ = try await coordinator.advance(
      meetingID: meetingID, to: .preparing,
      engine: EngineProvenance(engineID: "whisperkit", modelID: "tiny", modelVersion: "v1"),
      using: handle)
    try await coordinator.lock.release(handle)
    let handler = MeetingJobStatusHandler(store: store, dataRoot: root)

    let result = try await handler.execute(
      args: ["job_id": AnyCodable(meetingID.uuidString.lowercased())])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(json["status"] as? String, "preparing")
    let engine = try XCTUnwrap(json["engine"] as? [String: Any])
    XCTAssertEqual(Set(engine.keys), Set(["engine_id", "model_id"]))
    XCTAssertEqual(engine["engine_id"] as? String, "whisperkit")
    XCTAssertEqual(engine["model_id"] as? String, "tiny")
  }

  // MARK: - meeting_job_cancel

  func testJobCancelRequiresJobID() async throws {
    let store = MeetingStore(dataRoot: root)
    let handler = MeetingJobCancelHandler(store: store, dataRoot: root)
    let result = try await handler.execute(args: [:])

    XCTAssertTrue(result.isError)
    XCTAssertTrue(
      result.content.first?.text?.contains("Missing required parameter: job_id") ?? false)
  }

  func testJobCancelQueuedJobReturnsCancelledEnvelope() async throws {
    let store = MeetingStore(dataRoot: root)
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    let job = try await coordinator.enqueue(meetingID: meetingID)
    let handler = MeetingJobCancelHandler(store: store, dataRoot: root)

    let result = try await handler.execute(
      args: ["job_id": AnyCodable(job.jobID.uuidString.lowercased())])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(Set(json.keys), Set(["meeting_id", "status", "message"]))
    XCTAssertEqual(json["meeting_id"] as? String, meetingID.uuidString.lowercased())
    XCTAssertEqual(json["status"] as? String, "cancelled")
    XCTAssertEqual(json["message"] as? String, "Job cancelled.")

    let stored = try await coordinator.load(meetingID: meetingID)
    XCTAssertEqual(stored.status, .cancelled)
  }

  func testJobCancelActiveJobRequestsAndConfirmsCancellation() async throws {
    let store = MeetingStore(dataRoot: root)
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    _ = try await coordinator.advance(meetingID: meetingID, to: .preparing, using: handle)
    try await coordinator.lock.release(handle)
    let handler = MeetingJobCancelHandler(store: store, dataRoot: root)

    let result = try await handler.execute(
      args: ["job_id": AnyCodable(meetingID.uuidString.lowercased())])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(json["status"] as? String, "cancelled")

    let stored = try await coordinator.load(meetingID: meetingID)
    XCTAssertEqual(stored.status, .cancelled)
  }

  func testJobCancelTerminalJobReportsTerminalState() async throws {
    let store = MeetingStore(dataRoot: root)
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    _ = try await coordinator.advance(meetingID: meetingID, to: .preparing, using: handle)
    _ = try await coordinator.fail(
      meetingID: meetingID, classification: .retryable, code: "engine_failed",
      message: "boom", using: handle)
    try await coordinator.lock.release(handle)
    let handler = MeetingJobCancelHandler(store: store, dataRoot: root)

    let result = try await handler.execute(
      args: ["job_id": AnyCodable(meetingID.uuidString.lowercased())])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(Set(json.keys), Set(["status", "message"]))
    XCTAssertEqual(json["status"] as? String, "failed")
    XCTAssertEqual(json["message"] as? String, "Job is already in terminal state.")
  }
}
