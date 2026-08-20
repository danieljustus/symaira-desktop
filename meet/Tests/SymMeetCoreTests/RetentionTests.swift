import Foundation
import XCTest

@testable import SymMeetCore

final class RetentionTests: XCTestCase {
  private var root: URL!

  override func setUpWithError() throws {
    root = FileManager.default.temporaryDirectory.appending(
      path: "symmeet-retention-\(UUID().uuidString)")
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
  }

  override func tearDownWithError() throws {
    try? FileManager.default.removeItem(at: root)
  }

  func testDueRetentionDeletesDerivedArtifactsAndClearsJobState() async throws {
    let store = MeetingStore(dataRoot: root)
    let manifest = try makeManifest()
    try await store.create(manifest)
    let meetingID = manifest.meetingID.uuidString.lowercased()
    let normalized = root.appending(path: "meetings/\(meetingID)/audio/normalized.caf")
    try FileManager.default.createDirectory(
      at: normalized.deletingLastPathComponent(), withIntermediateDirectories: true)
    try Data("normalized media".utf8).write(to: normalized)

    let result = try await RetentionExecutor(store: store).execute(
      policy: .deleteAfter(Date(timeIntervalSince1970: 1)),
      meetingID: meetingID,
      now: Date(timeIntervalSince1970: 2)
    )
    let updated = try await store.load(meetingID: meetingID)

    XCTAssertEqual(result.state, .completed)
    XCTAssertNil(updated.job)
    XCTAssertFalse(FileManager.default.fileExists(atPath: normalized.path))
    XCTAssertFalse(
      FileManager.default.fileExists(
        atPath: root.appending(path: "meetings/\(meetingID)/transcript.md").path))
  }

  func testRetentionPartialFailureStaysVisibleAndRetryable() async throws {
    let store = MeetingStore(dataRoot: root)
    let manifest = try makeManifest()
    try await store.create(manifest)
    let meetingID = manifest.meetingID.uuidString.lowercased()
    let failingExecutor = RetentionExecutor(store: store) { url in
      if url.lastPathComponent == "transcript.md" { throw RemovalFailure.blocked }
      try FileManager.default.removeItem(at: url)
    }

    let failed = try await failingExecutor.execute(
      policy: .deleteAfter(Date(timeIntervalSince1970: 1)),
      meetingID: meetingID,
      now: Date(timeIntervalSince1970: 2)
    )

    XCTAssertEqual(failed.state, .failed)
    XCTAssertTrue(failed.remainingArtifacts.contains("transcript.md"))
    let afterFailure = try await store.load(meetingID: meetingID)
    XCTAssertNotNil(afterFailure.job)

    let retried = try await RetentionExecutor(store: store).execute(
      policy: .deleteAfter(Date(timeIntervalSince1970: 1)),
      meetingID: meetingID,
      now: Date(timeIntervalSince1970: 2)
    )
    XCTAssertEqual(retried.state, .completed)
    let afterRetry = try await store.load(meetingID: meetingID)
    XCTAssertNil(afterRetry.job)
  }

  func testDeleteAfterExportWaitsForConfirmedExport() async throws {
    let store = MeetingStore(dataRoot: root)
    let manifest = try makeManifest()
    try await store.create(manifest)

    let pending = try await RetentionExecutor(store: store).execute(
      policy: .deleteAfterExport,
      meetingID: manifest.meetingID.uuidString,
      exportState: .notExported
    )
    XCTAssertEqual(pending.state, .notDue)
    let afterPending = try await store.load(meetingID: manifest.meetingID.uuidString)
    XCTAssertNotNil(afterPending.job)
  }

  func testPermanentDeletionRequiresAnExplicitConfirmationType() async throws {
    let store = MeetingStore(dataRoot: root)
    let manifest = try makeManifest()
    try await store.create(manifest)
    try await store.trash(meetingID: manifest.meetingID.uuidString)

    let deleted = try await RetentionExecutor(store: store).permanentlyDelete(
      meetingID: manifest.meetingID.uuidString,
      confirmation: .commandLine
    )
    XCTAssertTrue(deleted)
  }

  private func makeManifest() throws -> MeetingManifest {
    MeetingManifest(
      meetingID: UUID(),
      source: .imported,
      createdAt: Date(timeIntervalSince1970: 0),
      updatedAt: Date(timeIntervalSince1970: 0),
      job: MeetingJob(jobID: UUID(), state: .processing),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep)
    )
  }
}

private enum RemovalFailure: Error, Sendable {
  case blocked
}
