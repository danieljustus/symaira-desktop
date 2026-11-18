import Foundation

public enum DictationRecordingMode: Equatable, Sendable {
  /// Recording lasts while the key is held.
  case pushToTalk
  /// Recording continues until the next key press.
  case handsFree
}

public enum DictationGestureState: Equatable, Sendable {
  case idle
  case recording(DictationRecordingMode)
  case processing
  /// Entered by Esc; stays until the next key press so the release of the key the user was
  /// still holding is not misread as a new gesture.
  case cancelled
}

public enum DictationGestureEvent: Equatable, Sendable {
  case keyDown(at: TimeInterval)
  case keyUp(at: TimeInterval)
  case escape
  /// The caller finished transcribing and delivering the result.
  case processingFinished
}

/// What the caller must do to the audio side after an event.
public enum DictationGestureEffect: Equatable, Sendable {
  case none
  case startRecording
  case stopRecordingAndProcess
  case cancelRecording
}

/// Push-to-talk / hands-free gesture logic as a pure state machine.
///
/// Time is injected through the events (no clock, no timer) so every rule is testable
/// deterministically. Rules:
/// - Hold the key to talk; releasing ends recording and starts processing.
/// - A press shorter than `shortPressLimit` is a tap, not speech: recording is cancelled
///   without processing. Two taps whose key-downs are within `doubleTapWindow` switch to
///   hands-free, which ends on the next key-down.
/// - Esc cancels any recording without processing.
/// - Key events while processing are ignored.
///
/// A short tap therefore briefly starts and cancels a recording; callers should treat
/// `startRecording` as cheap and revocable (e.g. delay showing UI).
public struct DictationGestureMachine: Equatable, Sendable {
  public static let defaultShortPressLimit: TimeInterval = 0.25
  public static let defaultDoubleTapWindow: TimeInterval = 0.35

  public private(set) var state: DictationGestureState = .idle
  public let shortPressLimit: TimeInterval
  public let doubleTapWindow: TimeInterval

  private var pressStartedAt: TimeInterval?
  private var lastTapStartedAt: TimeInterval?

  public init(
    shortPressLimit: TimeInterval = DictationGestureMachine.defaultShortPressLimit,
    doubleTapWindow: TimeInterval = DictationGestureMachine.defaultDoubleTapWindow
  ) {
    self.shortPressLimit = shortPressLimit
    self.doubleTapWindow = doubleTapWindow
  }

  /// Feeds one event and returns the effect the caller must perform.
  @discardableResult
  public mutating func handle(_ event: DictationGestureEvent) -> DictationGestureEffect {
    switch (state, event) {
    case (.idle, .keyDown(let time)), (.cancelled, .keyDown(let time)):
      return keyDownWhileIdle(at: time)
    case (.recording(.pushToTalk), .keyUp(let time)):
      return keyUpWhilePushToTalk(at: time)
    case (.recording(.handsFree), .keyDown):
      state = .processing
      return .stopRecordingAndProcess
    case (.recording, .escape):
      state = .cancelled
      pressStartedAt = nil
      lastTapStartedAt = nil
      return .cancelRecording
    case (_, .escape):
      // Esc with nothing to cancel still ends a pending double-tap window.
      lastTapStartedAt = nil
      return .none
    case (.processing, .processingFinished):
      state = .idle
      return .none
    default:
      // Auto-repeat key-downs while held, key-ups of hands-free presses, stray events.
      return .none
    }
  }

  private mutating func keyDownWhileIdle(at time: TimeInterval) -> DictationGestureEffect {
    if state == .idle, let tap = lastTapStartedAt, time >= tap, time - tap <= doubleTapWindow {
      lastTapStartedAt = nil
      pressStartedAt = nil
      state = .recording(.handsFree)
      return .startRecording
    }
    lastTapStartedAt = nil
    pressStartedAt = time
    state = .recording(.pushToTalk)
    return .startRecording
  }

  private mutating func keyUpWhilePushToTalk(at time: TimeInterval) -> DictationGestureEffect {
    let start = pressStartedAt
    pressStartedAt = nil
    if let start, time - start < shortPressLimit {
      lastTapStartedAt = start
      state = .idle
      return .cancelRecording
    }
    lastTapStartedAt = nil
    state = .processing
    return .stopRecordingAndProcess
  }
}
