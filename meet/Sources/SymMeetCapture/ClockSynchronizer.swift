import CoreMedia
import Foundation

/// A monotonic session clock that converts CMTime wall-clock values to
/// a session-relative offset anchored at the first sample received.
public actor ClockSynchronizer {
  private var sessionStart: CMTime?
  private var pauseAccumulated: CMTime = .zero

  /// The offset of the first sample received relative to the session clock origin.
  private var firstSampleOffset: CMTime?

  public init() {}

  /// Records a gap (pause) of the given wall-clock duration.
  public func recordPause(duration: CMTime) {
    pauseAccumulated = CMTimeAdd(pauseAccumulated, duration)
  }

  /// Converts a raw sample buffer presentation time to a session-relative timestamp.
  /// On the first call this anchors the session origin; subsequent calls are relative to that.
  /// Returns nil if the resulting time would be negative (before session start).
  public func sessionTime(for presentationTime: CMTime) -> CMTime? {
    if sessionStart == nil {
      sessionStart = presentationTime
      firstSampleOffset = .zero
    }
    guard let origin = sessionStart else { return nil }
    // presentationTime - origin - accumulated pauses
    let raw = CMTimeSubtract(presentationTime, origin)
    let adjusted = CMTimeSubtract(raw, pauseAccumulated)
    guard adjusted >= .zero else { return nil }
    return adjusted
  }

  /// Resets the clock — used when the session finishes or fails.
  public func reset() {
    sessionStart = nil
    pauseAccumulated = .zero
    firstSampleOffset = nil
  }

  /// The offset of the first sample in the session (always 0.0 when anchored normally).
  public var anchoredFirstSampleOffset: CMTime? { firstSampleOffset }
}
