import Foundation

/// How a recorded failure should be presented to a caller deciding whether to
/// retry automatically, prompt the user, or give up.
public enum JobFailureClassification: String, Codable, CaseIterable, Equatable, Sendable {
  case retryable
  case userActionRequired = "user_action_required"
  case permanent
}

/// One recorded failure. Failure history is append-only: a retry creates a
/// new attempt but never removes or overwrites a prior failure record.
public struct JobFailureRecord: Codable, Equatable, Sendable {
  public let attempt: Int
  public let occurredAt: Date
  public let classification: JobFailureClassification
  public let code: String
  public let message: String

  public init(
    attempt: Int,
    occurredAt: Date,
    classification: JobFailureClassification,
    code: String,
    message: String
  ) {
    self.attempt = attempt
    self.occurredAt = occurredAt
    self.classification = classification
    self.code = code
    self.message = message
  }

  private enum CodingKeys: String, CodingKey {
    case attempt
    case occurredAt = "occurred_at"
    case classification
    case code
    case message
  }
}

/// The durable, portable record of one meeting's transcription job.
///
/// This is the atomically-published ground truth for the job's current
/// status: ``JobCoordinator`` always writes the write-ahead journal entry
/// (see ``JobJournalEntry``) before publishing an updated record, so a
/// process killed between the two leaves this record describing the last
/// safely-completed state, never a state that never finished publishing.
public struct TranscriptionJob: Codable, Equatable, Sendable {
  public static let supportedSchemaVersion = 1

  public let schemaVersion: Int
  public let jobID: UUID
  public let meetingID: UUID
  public let status: JobStatus
  public let attempt: Int
  public let createdAt: Date
  public let updatedAt: Date
  public let engine: EngineProvenance?
  public let checkpoint: TranscriptionCheckpoint?
  public let failureHistory: [JobFailureRecord]

  public init(
    jobID: UUID,
    meetingID: UUID,
    status: JobStatus,
    attempt: Int,
    createdAt: Date,
    updatedAt: Date,
    engine: EngineProvenance? = nil,
    checkpoint: TranscriptionCheckpoint? = nil,
    failureHistory: [JobFailureRecord] = []
  ) {
    schemaVersion = Self.supportedSchemaVersion
    self.jobID = jobID
    self.meetingID = meetingID
    self.status = status
    self.attempt = attempt
    self.createdAt = createdAt
    self.updatedAt = updatedAt
    self.engine = engine
    self.checkpoint = checkpoint
    self.failureHistory = failureHistory
  }

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case schemaVersion = "schema_version"
    case jobID = "job_id"
    case meetingID = "meeting_id"
    case status
    case attempt
    case createdAt = "created_at"
    case updatedAt = "updated_at"
    case engine
    case checkpoint
    case failureHistory = "failure_history"
  }

  public init(from decoder: Decoder) throws {
    let container = try decoder.container(keyedBy: CodingKeys.self)
    let schemaVersion = try container.decode(Int.self, forKey: .schemaVersion)
    guard schemaVersion == Self.supportedSchemaVersion else {
      throw ContractError.unsupportedSchemaVersion(schemaVersion)
    }
    self.schemaVersion = schemaVersion
    jobID = try container.decode(UUID.self, forKey: .jobID)
    meetingID = try container.decode(UUID.self, forKey: .meetingID)
    status = try container.decode(JobStatus.self, forKey: .status)
    attempt = try container.decode(Int.self, forKey: .attempt)
    createdAt = try container.decode(Date.self, forKey: .createdAt)
    updatedAt = try container.decode(Date.self, forKey: .updatedAt)
    engine = try container.decodeIfPresent(EngineProvenance.self, forKey: .engine)
    checkpoint = try container.decodeIfPresent(TranscriptionCheckpoint.self, forKey: .checkpoint)
    failureHistory =
      try container.decodeIfPresent([JobFailureRecord].self, forKey: .failureHistory) ?? []
  }

  public func encode(to encoder: Encoder) throws {
    var container = encoder.container(keyedBy: CodingKeys.self)
    try container.encode(Self.supportedSchemaVersion, forKey: .schemaVersion)
    try container.encode(jobID, forKey: .jobID)
    try container.encode(meetingID, forKey: .meetingID)
    try container.encode(status, forKey: .status)
    try container.encode(attempt, forKey: .attempt)
    try container.encode(createdAt, forKey: .createdAt)
    try container.encode(updatedAt, forKey: .updatedAt)
    try container.encodeIfPresent(engine, forKey: .engine)
    try container.encodeIfPresent(checkpoint, forKey: .checkpoint)
    try container.encode(failureHistory, forKey: .failureHistory)
  }
}

/// One write-ahead entry in a job's append-only `events.jsonl` journal.
/// `fromStatus == nil` marks the job's creation (there is no prior status).
public struct JobJournalEntry: Codable, Equatable, Sendable {
  public static let supportedSchemaVersion = 1

  public let schemaVersion: Int
  public let entryID: UUID
  public let jobID: UUID
  public let meetingID: UUID
  public let attempt: Int
  public let recordedAt: Date
  public let fromStatus: JobStatus?
  public let toStatus: JobStatus
  public let note: String?

  public init(
    entryID: UUID = UUID(),
    jobID: UUID,
    meetingID: UUID,
    attempt: Int,
    recordedAt: Date,
    fromStatus: JobStatus?,
    toStatus: JobStatus,
    note: String? = nil
  ) {
    schemaVersion = Self.supportedSchemaVersion
    self.entryID = entryID
    self.jobID = jobID
    self.meetingID = meetingID
    self.attempt = attempt
    self.recordedAt = recordedAt
    self.fromStatus = fromStatus
    self.toStatus = toStatus
    self.note = note
  }

  private enum CodingKeys: String, CodingKey {
    case schemaVersion = "schema_version"
    case entryID = "entry_id"
    case jobID = "job_id"
    case meetingID = "meeting_id"
    case attempt
    case recordedAt = "recorded_at"
    case fromStatus = "from_status"
    case toStatus = "to_status"
    case note
  }
}

/// The stable, portable on-disk layout for job bookkeeping, kept separate
/// from meeting artifacts (`ArtifactLayout`) so job supervision state can be
/// rebuilt or discarded without touching meeting content.
public struct JobLayout: Equatable, Sendable {
  public static let recordFile = "job.json"
  public static let journalFile = "events.jsonl"
  public static let jobsDirectoryName = "jobs"
  public static let lockFile = ".job-coordinator.lock"

  public let dataRoot: URL

  public init(dataRoot: URL) {
    self.dataRoot = dataRoot.standardizedFileURL
  }

  public var jobsDirectory: URL {
    dataRoot.appending(path: Self.jobsDirectoryName, directoryHint: .isDirectory)
  }

  public var lockURL: URL {
    dataRoot.appending(path: Self.lockFile, directoryHint: .notDirectory)
  }

  public func jobDirectory(_ meetingID: String) -> URL {
    jobsDirectory.appending(path: meetingID, directoryHint: .isDirectory)
  }

  public func recordURL(in jobDirectory: URL) -> URL {
    jobDirectory.appending(path: Self.recordFile, directoryHint: .notDirectory)
  }

  public func journalURL(in jobDirectory: URL) -> URL {
    jobDirectory.appending(path: Self.journalFile, directoryHint: .notDirectory)
  }
}
