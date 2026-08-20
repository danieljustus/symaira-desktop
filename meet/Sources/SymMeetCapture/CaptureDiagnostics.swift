import Foundation

/// Diagnostic events recorded during a capture session.
public struct CaptureDiagnostics: Sendable {
  public struct Event: Sendable {
    public enum Kind: String, Sendable {
      case started
      case paused
      case resumed
      case stopped
      case interrupted
      case bufferOverrun
      case trackWriteFailed
      case permissionRevoked
    }

    public let kind: Kind
    public let timestamp: Date
    public let detail: String?

    public init(kind: Kind, timestamp: Date = Date(), detail: String? = nil) {
      self.kind = kind
      self.timestamp = timestamp
      self.detail = detail
    }
  }

  public private(set) var events: [Event] = []

  public mutating func record(_ event: Event) {
    events.append(event)
  }

  /// Total wall-clock recording duration (excluding pauses).
  public var recordingDuration: TimeInterval {
    var duration: TimeInterval = 0
    var startDate: Date?
    for event in events {
      switch event.kind {
      case .started:
        startDate = event.timestamp
      case .paused, .stopped, .interrupted:
        if let s = startDate {
          duration += event.timestamp.timeIntervalSince(s)
          startDate = nil
        }
      case .resumed:
        startDate = event.timestamp
      default:
        break
      }
    }
    return duration
  }

  /// Count of buffer overrun events.
  public var overrunCount: Int {
    events.filter { $0.kind == .bufferOverrun }.count
  }
}
