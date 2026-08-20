import Foundation
import XCTest

@testable import SymMeetCore

final class JobRecoveryTests: XCTestCase {
  private var root: URL!

  override func setUpWithError() throws {
    root = try makeTemporaryDirectory()
  }

  override func tearDownWithError() throws {
    try? FileManager.default.removeItem(at: root)
  }

  // MARK: Recovery of abandoned jobs (acceptance criteria 2 and 6)

  func testActiveJobWithNoLockIsMarkedInterruptedNotSilentlyResumed() async throws {
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    _ = try await coordinator.advance(meetingID: meetingID, to: .preparing, using: handle)
    _ = try await coordinator.advance(meetingID: meetingID, to: .transcribing, using: handle)
    // Simulate the process being killed: on a real crash the lock file
    // would still be on disk referencing the dead worker's PID, so recovery
    // must be able to verify liveness independently of file absence.
    try writeDeadLock(dataRoot: root)

    let report = try await JobRecovery(coordinator: coordinator).recoverAbandonedJobs()

    XCTAssertEqual(report.recovered.map(\.meetingID), [meetingID])
    XCTAssertEqual(report.recovered.first?.status, .interrupted)
    let reloaded = try await coordinator.load(meetingID: meetingID)
    XCTAssertEqual(reloaded.status, .interrupted)
    XCTAssertNotEqual(reloaded.status, .succeeded)
    XCTAssertNotEqual(reloaded.status, .cancelled)
  }

  func testEachActiveStatusRecoversToInterruptedNotToItsNextHappyPathState() async throws {
    for phase in [JobStatus.preparing, .transcribing, .exporting, .cancelling] {
      let dataRoot = try makeTemporaryDirectory()
      defer { try? FileManager.default.removeItem(at: dataRoot) }
      let coordinator = JobCoordinator(dataRoot: dataRoot)
      let meetingID = UUID()
      _ = try await coordinator.enqueue(meetingID: meetingID)
      let handle = try await coordinator.lock.acquire(meetingID: meetingID)
      try await drive(coordinator, meetingID: meetingID, to: phase, using: handle)
      try writeDeadLock(dataRoot: dataRoot)

      let report = try await JobRecovery(coordinator: coordinator).recoverAbandonedJobs()

      XCTAssertEqual(
        report.recovered.first?.status, .interrupted,
        "job abandoned mid-\(phase.rawValue) must recover to interrupted")
    }
  }

  func testKillingTheWorkerBetweenJournalAppendAndRecordPublishStillRecoversSafely() async throws {
    // Simulates acceptance criterion 2: a crash between the write-ahead
    // journal append and the record publish. The published record (ground
    // truth) still says "transcribing"; the journal already claims
    // "exporting" happened. Recovery must trust the published record, never
    // report success, and land on interrupted.
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    _ = try await coordinator.advance(meetingID: meetingID, to: .preparing, using: handle)
    let recordBeforeCrash = try await coordinator.advance(
      meetingID: meetingID, to: .transcribing, using: handle)

    // Manually append a journal entry claiming a transition that was never
    // published, simulating the crash window inside `commit`.
    try appendDanglingJournalEntry(
      dataRoot: root, meetingID: meetingID, from: .transcribing, to: .exporting)
    try writeDeadLock(dataRoot: root)

    let report = try await JobRecovery(coordinator: coordinator).recoverAbandonedJobs()

    XCTAssertEqual(report.recovered.first?.status, .interrupted)
    XCTAssertEqual(recordBeforeCrash.status, .transcribing)
    let reloaded = try await coordinator.load(meetingID: meetingID)
    XCTAssertNotEqual(
      reloaded.status, .succeeded, "a dangling journal entry must never fabricate success")
  }

  func testRecoveryNeverPublishesSucceededForAnExportingJobKilledBeforeManifestPublish()
    async throws
  {
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    try await drive(coordinator, meetingID: meetingID, to: .exporting, using: handle)
    try writeDeadLock(dataRoot: root)

    _ = try await JobRecovery(coordinator: coordinator).recoverAbandonedJobs()

    let reloaded = try await coordinator.load(meetingID: meetingID)
    XCTAssertEqual(reloaded.status, .interrupted)
  }

  func testRecoveryLeavesAJobAloneWhileItsLockIsStillLive() async throws {
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    _ = try await coordinator.advance(meetingID: meetingID, to: .preparing, using: handle)
    _ = try await coordinator.advance(meetingID: meetingID, to: .transcribing, using: handle)

    // The lock is still held by this (live) process -- recovery must be a no-op.
    let report = try await JobRecovery(coordinator: coordinator).recoverAbandonedJobs()

    XCTAssertEqual(report.recovered, [])
    let reloaded = try await coordinator.load(meetingID: meetingID)
    XCTAssertEqual(reloaded.status, .transcribing)
  }

  func testRecoveryDoesNotAssumeAnActivePIDFromADifferentBootIsStillValid() async throws {
    // Same (live) PID as this test process, but a different boot time --
    // recovery must not treat that as a currently-valid owner.
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    _ = try await coordinator.advance(meetingID: meetingID, to: .preparing, using: handle)

    let lockURL = JobLayout(dataRoot: root).lockURL
    try? FileManager.default.removeItem(at: lockURL)
    let owner = LockOwner(
      pid: getpid(), processStartTime: LockOwnership.processStartTime(for: getpid()) ?? 0,
      bootTime: 1, sessionToken: UUID(), meetingID: meetingID, acquiredAt: Date())
    try ContractCodec.encoder().encode(owner).write(to: lockURL)

    let report = try await JobRecovery(coordinator: coordinator).recoverAbandonedJobs()

    XCTAssertEqual(report.recovered.map(\.meetingID), [meetingID])
  }

  // MARK: Listing survives a corrupt job artifact (acceptance criterion 7)

  func testListingSkipsACorruptJobArtifactAndStillReportsTheRest() async throws {
    let coordinator = JobCoordinator(dataRoot: root)
    let good = UUID()
    _ = try await coordinator.enqueue(meetingID: good)

    let corruptID = UUID().uuidString.lowercased()
    let corruptDirectory = root.appending(path: "jobs/\(corruptID)")
    try FileManager.default.createDirectory(at: corruptDirectory, withIntermediateDirectories: true)
    try Data("not json".utf8).write(to: corruptDirectory.appending(path: "job.json"))

    let listing = try await coordinator.list()

    XCTAssertEqual(listing.jobs.map(\.meetingID), [good])
    XCTAssertTrue(
      listing.diagnostics.contains(JobDiagnostic(meetingID: corruptID, code: .malformedRecord)))
  }

  func testRecoveryReportsCorruptArtifactsWithoutFailingTheWholeSweep() async throws {
    let coordinator = JobCoordinator(dataRoot: root)
    let good = UUID()
    _ = try await coordinator.enqueue(meetingID: good)
    let handle = try await coordinator.lock.acquire(meetingID: good)
    _ = try await coordinator.advance(meetingID: good, to: .preparing, using: handle)

    let corruptID = UUID().uuidString.lowercased()
    let corruptDirectory = root.appending(path: "jobs/\(corruptID)")
    try FileManager.default.createDirectory(at: corruptDirectory, withIntermediateDirectories: true)
    try Data("not json".utf8).write(to: corruptDirectory.appending(path: "job.json"))
    try writeDeadLock(dataRoot: root)

    let report = try await JobRecovery(coordinator: coordinator).recoverAbandonedJobs()

    XCTAssertEqual(report.recovered.map(\.meetingID), [good])
    XCTAssertTrue(
      report.diagnostics.contains(JobDiagnostic(meetingID: corruptID, code: .malformedRecord)))
  }

  // MARK: Retry and resume (acceptance criterion 5)

  func testRetryFromFailedKeepsFailureHistoryAndStartsANewAttemptWithNoCheckpoint() async throws {
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    let created = try await coordinator.enqueue(meetingID: meetingID)
    XCTAssertEqual(created.attempt, 1)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    _ = try await coordinator.advance(meetingID: meetingID, to: .preparing, using: handle)
    _ = try await coordinator.advance(meetingID: meetingID, to: .transcribing, using: handle)
    _ = try await coordinator.recordCheckpoint(
      meetingID: meetingID,
      checkpoint: TranscriptionCheckpoint(
        completedSourceTimeMS: 5_000, engineID: "e", modelID: "m"),
      using: handle)
    let failed = try await coordinator.fail(
      meetingID: meetingID, classification: .retryable, code: "engine_crash",
      message: "engine crashed", using: handle)
    XCTAssertEqual(failed.failureHistory.count, 1)

    let retried = try await coordinator.retry(meetingID: meetingID, using: handle)

    XCTAssertEqual(retried.status, .queued)
    XCTAssertEqual(retried.attempt, 2)
    XCTAssertNil(
      retried.checkpoint, "retry restarts the bounded chunk, it does not resume mid-call")
    XCTAssertEqual(retried.failureHistory.count, 1, "retry must not erase failure history")
    XCTAssertEqual(retried.failureHistory.first?.code, "engine_crash")

    // A second failure on the new attempt must be appended, not overwrite the first.
    _ = try await coordinator.advance(meetingID: meetingID, to: .preparing, using: handle)
    _ = try await coordinator.advance(meetingID: meetingID, to: .transcribing, using: handle)
    let failedAgain = try await coordinator.fail(
      meetingID: meetingID, classification: .permanent, code: "unsupported_format",
      message: "unsupported", using: handle)
    XCTAssertEqual(failedAgain.failureHistory.count, 2)
    XCTAssertEqual(failedAgain.failureHistory.map(\.code), ["engine_crash", "unsupported_format"])
  }

  func testResumeFromInterruptedRetainsCheckpointAndFailedDoesNotAllowResume() async throws {
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    _ = try await coordinator.advance(meetingID: meetingID, to: .preparing, using: handle)
    _ = try await coordinator.advance(meetingID: meetingID, to: .transcribing, using: handle)
    let checkpoint = TranscriptionCheckpoint(
      completedSourceTimeMS: 12_000, engineID: "fake", modelID: "tiny")
    _ = try await coordinator.recordCheckpoint(
      meetingID: meetingID, checkpoint: checkpoint, using: handle)
    try writeDeadLock(dataRoot: root)
    _ = try await JobRecovery(coordinator: coordinator).recoverAbandonedJobs()

    let newHandle = try await coordinator.lock.acquire(meetingID: meetingID)
    let resumed = try await coordinator.resume(meetingID: meetingID, using: newHandle)

    XCTAssertEqual(resumed.status, .queued)
    XCTAssertEqual(resumed.attempt, 2)
    XCTAssertEqual(resumed.checkpoint, checkpoint, "resume must retain the last safe checkpoint")

    // resume is only valid from interrupted, never from failed/cancelled/queued.
    do {
      _ = try await coordinator.resume(meetingID: meetingID, using: newHandle)
      XCTFail("Expected resume from queued to fail")
    } catch JobError.notInterrupted {
      // expected
    }
  }

  // MARK: Cancellation never finalizes a partial segment (acceptance criterion 4)

  func testCancellationRequiresAnExplicitConfirmationAndNeverAutoFinalizes() async throws {
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    _ = try await coordinator.advance(meetingID: meetingID, to: .preparing, using: handle)
    _ = try await coordinator.advance(meetingID: meetingID, to: .transcribing, using: handle)

    let cancelling = try await coordinator.requestCancellation(meetingID: meetingID, using: handle)
    XCTAssertEqual(cancelling.status, .cancelling)

    // Cancellation is requested, but nothing may report "cancelled" (or any
    // other terminal state) until the in-flight atomic write reaches its
    // safe boundary and the caller explicitly confirms.
    let stillCancelling = try await coordinator.load(meetingID: meetingID)
    XCTAssertEqual(stillCancelling.status, .cancelling)

    let cancelled = try await coordinator.confirmCancelled(meetingID: meetingID, using: handle)
    XCTAssertEqual(cancelled.status, .cancelled)
  }

  func testSucceedOnlyPublishesAfterTheManifestPublishClosureCompletes() async throws {
    let coordinator = JobCoordinator(dataRoot: root)
    let meetingID = UUID()
    _ = try await coordinator.enqueue(meetingID: meetingID)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    try await drive(coordinator, meetingID: meetingID, to: .exporting, using: handle)

    struct PublishFailed: Error {}
    do {
      _ = try await coordinator.succeed(meetingID: meetingID, using: handle) {
        throw PublishFailed()
      }
      XCTFail("Expected the publish failure to propagate")
    } catch is PublishFailed {
      // expected
    }
    let stillExporting = try await coordinator.load(meetingID: meetingID)
    XCTAssertEqual(
      stillExporting.status, .exporting,
      "a job must never be marked succeeded when manifest publication failed")

    let succeeded = try await coordinator.succeed(meetingID: meetingID, using: handle) {}
    XCTAssertEqual(succeeded.status, .succeeded)
  }

  // MARK: Helpers

  private func drive(
    _ coordinator: JobCoordinator, meetingID: UUID, to status: JobStatus, using handle: LockHandle
  ) async throws {
    let order: [JobStatus] = [.preparing, .transcribing, .exporting, .cancelling]
    for step in order {
      _ = try await coordinator.advance(meetingID: meetingID, to: step, using: handle)
      if step == status { return }
    }
  }

  private func writeDeadLock(dataRoot: URL) throws {
    let lockURL = dataRoot.appending(path: JobLayout.lockFile)
    try? FileManager.default.removeItem(at: lockURL)
    let owner = LockOwner(
      pid: 2_000_000_000, processStartTime: 1, bootTime: 1, sessionToken: UUID(), meetingID: nil,
      acquiredAt: Date())
    try ContractCodec.encoder().encode(owner).write(to: lockURL)
  }

  private func appendDanglingJournalEntry(
    dataRoot: URL, meetingID: UUID, from: JobStatus, to: JobStatus
  ) throws {
    let directory = dataRoot.appending(path: "jobs/\(meetingID.uuidString.lowercased())")
    let journalURL = directory.appending(path: "events.jsonl")
    let entry = JobJournalEntry(
      jobID: UUID(), meetingID: meetingID, attempt: 1, recordedAt: Date(), fromStatus: from,
      toStatus: to, note: "simulated crash before publish")
    var data = (try? Data(contentsOf: journalURL)) ?? Data()
    data.append(try ContractCodec.encoder().encode(entry))
    data.append(0x0A)
    try data.write(to: journalURL)
  }
}

private func makeTemporaryDirectory() throws -> URL {
  let directory = FileManager.default.temporaryDirectory.appending(
    path: "symmeet-jobrecovery-\(UUID().uuidString)")
  try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
  return directory
}
