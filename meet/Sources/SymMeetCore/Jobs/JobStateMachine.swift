import Foundation

/// The durable status of a transcription job.
///
/// `interrupted` is not reachable by ordinary progress: it is written only by
/// ``JobRecovery`` when an active job (`preparing`, `transcribing`,
/// `exporting`, or `cancelling`) is found with no live owning process. It
/// exists so recovery never silently resumes a job as if nothing happened.
public enum JobStatus: String, Codable, CaseIterable, Equatable, Sendable {
  case queued
  case preparing
  case transcribing
  case exporting
  case succeeded
  case failed
  case cancelling
  case cancelled
  case interrupted

  /// Statuses in which a process is expected to be actively mutating the job.
  /// A job found in one of these statuses with no live lock owner is abandoned.
  public var isActive: Bool {
    switch self {
    case .preparing, .transcribing, .exporting, .cancelling: true
    case .queued, .succeeded, .failed, .cancelled, .interrupted: false
    }
  }

  /// Statuses with no further automatic progress: leaving them requires an
  /// explicit retry or resume action.
  public var isTerminal: Bool {
    switch self {
    case .succeeded, .failed, .cancelled, .interrupted: true
    case .queued, .preparing, .transcribing, .exporting, .cancelling: false
    }
  }
}

/// Validates the allowed transitions of the transcription job lifecycle.
///
/// ```text
/// queued -> preparing -> transcribing -> exporting -> succeeded
///        -> cancelled                (no in-flight write to wait for)
///                     -> failed
///                     -> cancelling -> cancelled
/// failed | cancelled | interrupted -> queued   (explicit retry/resume only)
/// preparing | transcribing | exporting | cancelling -> interrupted
///                                          (recovery only, never automatic progress)
/// ```
public enum JobStateMachine {
  /// The exhaustive set of allowed `(from, to)` transitions.
  public static let allowedTransitions: Set<Transition> = [
    Transition(.queued, .preparing),
    Transition(.queued, .cancelled),

    Transition(.preparing, .transcribing),
    Transition(.preparing, .failed),
    Transition(.preparing, .cancelling),
    Transition(.preparing, .interrupted),

    Transition(.transcribing, .exporting),
    Transition(.transcribing, .failed),
    Transition(.transcribing, .cancelling),
    Transition(.transcribing, .interrupted),

    Transition(.exporting, .succeeded),
    Transition(.exporting, .failed),
    Transition(.exporting, .cancelling),
    Transition(.exporting, .interrupted),

    Transition(.cancelling, .cancelled),
    Transition(.cancelling, .interrupted),

    Transition(.failed, .queued),
    Transition(.cancelled, .queued),
    Transition(.interrupted, .queued),
  ]

  public struct Transition: Hashable, Sendable {
    public let from: JobStatus
    public let to: JobStatus

    public init(_ from: JobStatus, _ to: JobStatus) {
      self.from = from
      self.to = to
    }
  }

  public static func isAllowed(from: JobStatus, to: JobStatus) -> Bool {
    allowedTransitions.contains(Transition(from, to))
  }

  /// Validates a transition, throwing ``JobError/invalidTransition(from:to:)``
  /// when it is not allowed.
  public static func validate(from: JobStatus, to: JobStatus) throws {
    guard isAllowed(from: from, to: to) else {
      throw JobError.invalidTransition(from: from, to: to)
    }
  }
}
