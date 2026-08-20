import Foundation

/// The set of states a capture session can occupy.
public enum CaptureState: Equatable, Sendable {
  case idle
  case authorizing
  case starting
  case recording
  case pausing
  case paused
  case stopping
  case finished
  case failed(reason: String)
  case interrupted(reason: String)
}

/// Legal state transitions for a capture session.
/// Returns the new state if the transition is valid, nil otherwise.
public struct CaptureStateMachine: Sendable {
  public private(set) var state: CaptureState

  public init(initial: CaptureState = .idle) {
    self.state = initial
  }

  /// Attempts to transition to `next`. Returns false if the transition is illegal.
  @discardableResult
  public mutating func transition(to next: CaptureState) -> Bool {
    guard isValid(from: state, to: next) else { return false }
    state = next
    return true
  }

  // MARK: - Validity table

  private func isValid(from current: CaptureState, to next: CaptureState) -> Bool {
    switch (current, next) {
    // Happy path
    case (.idle, .authorizing): true
    case (.authorizing, .starting): true
    case (.starting, .recording): true
    case (.recording, .pausing): true
    case (.pausing, .paused): true
    case (.paused, .starting): true  // resume
    case (.recording, .stopping): true
    case (.paused, .stopping): true
    case (.stopping, .finished): true
    // Failure / interruption from any active state
    case (_, .failed): isActive(current)
    case (_, .interrupted): isActive(current)
    // Re-entrant start is rejected (returns false)
    default: false
    }
  }

  private func isActive(_ state: CaptureState) -> Bool {
    switch state {
    case .authorizing, .starting, .recording, .pausing, .paused, .stopping: true
    default: false
    }
  }
}

// MARK: - CustomStringConvertible

extension CaptureState: CustomStringConvertible {
  public var description: String {
    switch self {
    case .idle: "idle"
    case .authorizing: "authorizing"
    case .starting: "starting"
    case .recording: "recording"
    case .pausing: "pausing"
    case .paused: "paused"
    case .stopping: "stopping"
    case .finished: "finished"
    case .failed(let r): "failed(\(r))"
    case .interrupted(let r): "interrupted(\(r))"
    }
  }
}
