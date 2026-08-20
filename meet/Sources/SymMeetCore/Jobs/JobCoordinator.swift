import Foundation

public enum JobDiagnosticCode: String, Codable, Equatable, Sendable {
  case invalidJobDirectory = "invalid_job_directory"
  case malformedRecord = "malformed_record"
}

public struct JobDiagnostic: Codable, Equatable, Sendable {
  public let meetingID: String
  public let code: JobDiagnosticCode

  public init(meetingID: String, code: JobDiagnosticCode) {
    self.meetingID = meetingID
    self.code = code
  }
}

/// A job listing that keeps working when individual job artifacts are
/// corrupt: unreadable records are reported as diagnostics instead of
/// failing the whole listing.
public struct JobListResult: Equatable, Sendable {
  public let jobs: [TranscriptionJob]
  public let diagnostics: [JobDiagnostic]

  public init(jobs: [TranscriptionJob], diagnostics: [JobDiagnostic]) {
    self.jobs = jobs
    self.diagnostics = diagnostics
  }
}

/// Orchestrates the durable lifecycle of transcription jobs: one job record
/// and one write-ahead `events.jsonl` journal per meeting, guarded by a
/// single ``DataRootLock`` per data root.
///
/// Every mutating call other than ``enqueue(meetingID:)`` requires a
/// ``LockHandle`` obtained from ``lock``, and re-verifies against the
/// on-disk lock before writing -- a caller can never mutate job state
/// without provably holding the current lock.
public actor JobCoordinator {
  public let layout: JobLayout
  public let lock: DataRootLock

  public init(dataRoot: URL) {
    layout = JobLayout(dataRoot: dataRoot)
    lock = DataRootLock(dataRoot: dataRoot)
  }

  // MARK: Creation and reads

  @discardableResult
  public func enqueue(meetingID: UUID) async throws -> TranscriptionJob {
    try prepareDirectory(layout.jobsDirectory)
    let directory = layout.jobDirectory(normalizedKey(meetingID))
    guard !FileManager.default.fileExists(atPath: directory.path) else {
      throw JobError.alreadyExists
    }
    do {
      try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false)
    } catch {
      throw JobError.operationFailed
    }

    do {
      let now = Date()
      let job = TranscriptionJob(
        jobID: UUID(), meetingID: meetingID, status: .queued, attempt: 1, createdAt: now,
        updatedAt: now)
      let entry = JobJournalEntry(
        jobID: job.jobID, meetingID: meetingID, attempt: 1, recordedAt: now, fromStatus: nil,
        toStatus: .queued, note: "created")
      try appendJournal(entry, in: directory)
      try publishRecord(job, in: directory)
      return job
    } catch {
      try? FileManager.default.removeItem(at: directory)
      throw error is JobError ? error : JobError.operationFailed
    }
  }

  public func load(meetingID: UUID) async throws -> TranscriptionJob {
    let directory = try directoryRequiringExisting(meetingID)
    return try readRecord(in: directory)
  }

  public func journal(meetingID: UUID) async throws -> [JobJournalEntry] {
    let directory = try directoryRequiringExisting(meetingID)
    let url = layout.journalURL(in: directory)
    guard let data = try? Data(contentsOf: url), !data.isEmpty else { return [] }
    do {
      return try data.split(separator: 0x0A).map {
        try ContractCodec.decoder().decode(JobJournalEntry.self, from: Data($0))
      }
    } catch {
      throw JobError.corruptRecord
    }
  }

  public func list() async throws -> JobListResult {
    try prepareDirectory(layout.jobsDirectory)
    let entries: [URL]
    do {
      entries = try FileManager.default.contentsOfDirectory(
        at: layout.jobsDirectory, includingPropertiesForKeys: [.isDirectoryKey],
        options: [.skipsHiddenFiles])
    } catch {
      throw JobError.operationFailed
    }

    var jobs: [TranscriptionJob] = []
    var diagnostics: [JobDiagnostic] = []

    for entry in entries.sorted(by: { $0.lastPathComponent < $1.lastPathComponent }) {
      let key = entry.lastPathComponent
      guard UUID(uuidString: key) != nil else {
        diagnostics.append(JobDiagnostic(meetingID: key, code: .invalidJobDirectory))
        continue
      }
      do {
        jobs.append(try readRecord(in: entry))
      } catch {
        diagnostics.append(JobDiagnostic(meetingID: key, code: .malformedRecord))
      }
    }
    return JobListResult(jobs: jobs, diagnostics: diagnostics)
  }

  /// The earliest-queued job, if any (FIFO; no scheduler beyond arrival order).
  public func nextQueued() async throws -> TranscriptionJob? {
    try await list().jobs
      .filter { $0.status == .queued }
      .sorted { $0.createdAt < $1.createdAt }
      .first
  }

  // MARK: Mutating lifecycle operations (require a held lock)

  /// Moves the job to `status`, optionally attaching engine provenance or a
  /// refreshed checkpoint. Validated against ``JobStateMachine``.
  @discardableResult
  public func advance(
    meetingID: UUID,
    to status: JobStatus,
    engine: EngineProvenance? = nil,
    checkpoint: TranscriptionCheckpoint? = nil,
    using handle: LockHandle,
    note: String? = nil
  ) async throws -> TranscriptionJob {
    try await requireHeldLock(handle)
    let directory = try directoryRequiringExisting(meetingID)
    let current = try readRecord(in: directory)
    let now = Date()
    let next = TranscriptionJob(
      jobID: current.jobID,
      meetingID: current.meetingID,
      status: status,
      attempt: current.attempt,
      createdAt: current.createdAt,
      updatedAt: now,
      engine: engine ?? current.engine,
      checkpoint: checkpoint ?? current.checkpoint,
      failureHistory: current.failureHistory)
    return try commit(next: next, from: current.status, jobDirectory: directory, note: note)
  }

  /// Records progress without changing status. Only valid while the job is
  /// actively being worked (`preparing`, `transcribing`, `exporting`, or
  /// `cancelling`), so a resumed job always knows where to continue.
  @discardableResult
  public func recordCheckpoint(
    meetingID: UUID,
    checkpoint: TranscriptionCheckpoint,
    using handle: LockHandle
  ) async throws -> TranscriptionJob {
    try await requireHeldLock(handle)
    let directory = try directoryRequiringExisting(meetingID)
    let current = try readRecord(in: directory)
    guard current.status.isActive else {
      throw JobError.invalidTransition(from: current.status, to: current.status)
    }
    let now = Date()
    let next = TranscriptionJob(
      jobID: current.jobID, meetingID: current.meetingID, status: current.status,
      attempt: current.attempt, createdAt: current.createdAt, updatedAt: now,
      engine: current.engine, checkpoint: checkpoint, failureHistory: current.failureHistory)
    let entry = JobJournalEntry(
      jobID: next.jobID, meetingID: next.meetingID, attempt: next.attempt, recordedAt: now,
      fromStatus: current.status, toStatus: current.status, note: "checkpoint")
    try appendJournal(entry, in: directory)
    try publishRecord(next, in: directory)
    return next
  }

  /// Records a failure and moves the job to `failed`. Failure history is
  /// append-only: prior failures from earlier attempts are always kept.
  @discardableResult
  public func fail(
    meetingID: UUID,
    classification: JobFailureClassification,
    code: String,
    message: String,
    using handle: LockHandle
  ) async throws -> TranscriptionJob {
    try await requireHeldLock(handle)
    let directory = try directoryRequiringExisting(meetingID)
    let current = try readRecord(in: directory)
    let now = Date()
    let failure = JobFailureRecord(
      attempt: current.attempt, occurredAt: now, classification: classification, code: code,
      message: message)
    let next = TranscriptionJob(
      jobID: current.jobID, meetingID: current.meetingID, status: .failed,
      attempt: current.attempt, createdAt: current.createdAt, updatedAt: now,
      engine: current.engine, checkpoint: current.checkpoint,
      failureHistory: current.failureHistory + [failure])
    return try commit(next: next, from: current.status, jobDirectory: directory, note: code)
  }

  /// Requests cancellation. The caller must let whatever atomic write is in
  /// flight finish, then call ``confirmCancelled(meetingID:using:)`` -- this
  /// method never finalizes anything by itself.
  @discardableResult
  public func requestCancellation(meetingID: UUID, using handle: LockHandle) async throws
    -> TranscriptionJob
  {
    try await advance(
      meetingID: meetingID, to: .cancelling, using: handle, note: "cancellation requested")
  }

  /// Confirms cancellation after the caller has reached a safe write
  /// boundary. Only valid from `cancelling`.
  @discardableResult
  public func confirmCancelled(meetingID: UUID, using handle: LockHandle) async throws
    -> TranscriptionJob
  {
    try await advance(
      meetingID: meetingID, to: .cancelled, using: handle,
      note: "cancellation confirmed after safe write boundary")
  }

  /// Marks the job succeeded only after `publish` (the caller's export and
  /// manifest-publication work) completes without throwing. If `publish`
  /// throws, the job is never marked succeeded.
  @discardableResult
  public func succeed(
    meetingID: UUID,
    using handle: LockHandle,
    afterPublishing publish: @Sendable () async throws -> Void
  ) async throws -> TranscriptionJob {
    try await publish()
    return try await advance(
      meetingID: meetingID, to: .succeeded, using: handle,
      note: "exports and manifest published")
  }

  /// Starts a fresh attempt from `failed`, `cancelled`, or `interrupted`.
  /// The checkpoint is discarded (a full restart) but failure history is
  /// preserved. Never invoked automatically -- always an explicit caller
  /// action.
  @discardableResult
  public func retry(meetingID: UUID, using handle: LockHandle) async throws -> TranscriptionJob {
    try await requireHeldLock(handle)
    let directory = try directoryRequiringExisting(meetingID)
    let current = try readRecord(in: directory)
    guard [.failed, .cancelled, .interrupted].contains(current.status) else {
      throw JobError.notRetryable
    }
    let now = Date()
    let next = TranscriptionJob(
      jobID: current.jobID, meetingID: current.meetingID, status: .queued,
      attempt: current.attempt + 1, createdAt: current.createdAt, updatedAt: now,
      engine: current.engine, checkpoint: nil, failureHistory: current.failureHistory)
    return try commit(next: next, from: current.status, jobDirectory: directory, note: "retry")
  }

  /// Starts a fresh attempt from `interrupted` only, retaining the last
  /// checkpoint so the new attempt can continue from the completed source
  /// time instead of restarting from zero. Never invoked automatically.
  @discardableResult
  public func resume(meetingID: UUID, using handle: LockHandle) async throws -> TranscriptionJob {
    try await requireHeldLock(handle)
    let directory = try directoryRequiringExisting(meetingID)
    let current = try readRecord(in: directory)
    guard current.status == .interrupted else { throw JobError.notInterrupted }
    let now = Date()
    let next = TranscriptionJob(
      jobID: current.jobID, meetingID: current.meetingID, status: .queued,
      attempt: current.attempt + 1, createdAt: current.createdAt, updatedAt: now,
      engine: current.engine, checkpoint: current.checkpoint,
      failureHistory: current.failureHistory)
    return try commit(
      next: next, from: current.status, jobDirectory: directory, note: "resume from checkpoint")
  }

  /// Converts an abandoned active job into `interrupted`. Reserved for
  /// ``JobRecovery``, which only calls this while it holds the data-root
  /// lock itself (proving no other process can legitimately still be
  /// working on the job).
  @discardableResult
  func markInterrupted(meetingID: UUID, using handle: LockHandle, reason: String) async throws
    -> TranscriptionJob
  {
    try await requireHeldLock(handle)
    let directory = try directoryRequiringExisting(meetingID)
    let current = try readRecord(in: directory)
    let now = Date()
    let next = TranscriptionJob(
      jobID: current.jobID, meetingID: current.meetingID, status: .interrupted,
      attempt: current.attempt, createdAt: current.createdAt, updatedAt: now,
      engine: current.engine, checkpoint: current.checkpoint,
      failureHistory: current.failureHistory)
    return try commit(next: next, from: current.status, jobDirectory: directory, note: reason)
  }

  // MARK: Private helpers

  /// Write-ahead-then-publish: the journal entry always lands before the
  /// record does, so a process killed in between leaves the record
  /// describing the last state that actually finished publishing.
  private func commit(
    next: TranscriptionJob, from: JobStatus, jobDirectory: URL, note: String?
  ) throws -> TranscriptionJob {
    try JobStateMachine.validate(from: from, to: next.status)
    let entry = JobJournalEntry(
      jobID: next.jobID, meetingID: next.meetingID, attempt: next.attempt,
      recordedAt: next.updatedAt, fromStatus: from, toStatus: next.status, note: note)
    try appendJournal(entry, in: jobDirectory)
    try publishRecord(next, in: jobDirectory)
    return next
  }

  private func requireHeldLock(_ handle: LockHandle) async throws {
    guard let onDisk = try await lock.currentOwner(),
      onDisk.sessionToken == handle.owner.sessionToken,
      onDisk.pid == handle.owner.pid
    else {
      throw JobError.lockNotOwned
    }
  }

  private func readRecord(in directory: URL) throws -> TranscriptionJob {
    let url = layout.recordURL(in: directory)
    do {
      return try ContractCodec.decoder().decode(TranscriptionJob.self, from: Data(contentsOf: url))
    } catch {
      throw error is JobError ? error : JobError.corruptRecord
    }
  }

  private func publishRecord(_ job: TranscriptionJob, in directory: URL) throws {
    do {
      let data = try ContractCodec.encoder(prettyPrinted: true).encode(job)
      try AtomicFileWriter.write(data, to: layout.recordURL(in: directory))
    } catch {
      throw error is JobError ? error : JobError.operationFailed
    }
  }

  private func appendJournal(_ entry: JobJournalEntry, in directory: URL) throws {
    let url = layout.journalURL(in: directory)
    let existing = (try? Data(contentsOf: url)) ?? Data()
    do {
      let entryData = try ContractCodec.encoder().encode(entry)
      var updated = existing
      updated.append(entryData)
      updated.append(0x0A)
      try AtomicFileWriter.write(updated, to: url)
    } catch {
      throw error is JobError ? error : JobError.operationFailed
    }
  }

  private func prepareDirectory(_ directory: URL) throws {
    do {
      try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    } catch {
      throw JobError.operationFailed
    }
  }

  private func directoryRequiringExisting(_ meetingID: UUID) throws -> URL {
    let directory = layout.jobDirectory(normalizedKey(meetingID))
    var isDirectory: ObjCBool = false
    guard FileManager.default.fileExists(atPath: directory.path, isDirectory: &isDirectory),
      isDirectory.boolValue
    else {
      throw JobError.notFound
    }
    return directory
  }

  private func normalizedKey(_ meetingID: UUID) -> String {
    meetingID.uuidString.lowercased()
  }
}
