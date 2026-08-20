import Foundation

/// Errors raised by the durable transcription-job subsystem (state machine,
/// locking, coordination, and recovery).
public enum JobError: Error, Equatable, LocalizedError, Sendable {
  case invalidTransition(from: JobStatus, to: JobStatus)
  case lockHeld(ownerPID: Int32)
  case lockNotOwned
  case notFound
  case alreadyExists
  case corruptRecord
  case notInterrupted
  case notRetryable
  case operationFailed

  public var errorDescription: String? {
    switch self {
    case .invalidTransition(let from, let to):
      "Invalid job state transition from \(from.rawValue) to \(to.rawValue)."
    case .lockHeld(let ownerPID):
      "The data root is locked by another active process (pid \(ownerPID))."
    case .lockNotOwned:
      "The lock cannot be released because it is not owned by this handle."
    case .notFound:
      "No job record exists for the requested meeting."
    case .alreadyExists:
      "A job record already exists for this meeting."
    case .corruptRecord:
      "The job record is malformed and cannot be decoded."
    case .notInterrupted:
      "Resume is only valid for a job that recovery has marked interrupted."
    case .notRetryable:
      "Retry is only valid for a job in failed, cancelled, or interrupted status."
    case .operationFailed:
      "The job operation could not be completed."
    }
  }
}
