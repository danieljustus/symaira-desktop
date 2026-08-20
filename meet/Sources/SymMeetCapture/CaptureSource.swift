import Foundation

/// A discoverable audio source that SymMeet can capture from.
public struct CaptureSource: Codable, Equatable, Sendable, Identifiable {
  public enum Kind: String, Codable, Equatable, Sendable {
    case display
    case application
    case microphone
  }

  public let id: String
  public let kind: Kind
  public let displayName: String
  /// The bundle identifier of the application, if applicable.
  public let bundleID: String?
  /// Whether the source is currently running and capturable.
  public let isActive: Bool
  /// Whether system audio output is available on this source.
  public let supportsSystemAudio: Bool

  public init(
    id: String,
    kind: Kind,
    displayName: String,
    bundleID: String? = nil,
    isActive: Bool,
    supportsSystemAudio: Bool
  ) {
    self.id = id
    self.kind = kind
    self.displayName = displayName
    self.bundleID = bundleID
    self.isActive = isActive
    self.supportsSystemAudio = supportsSystemAudio
  }
}

/// The full set of audio sources available for capture.
public struct CaptureSourceList: Codable, Equatable, Sendable {
  public let displays: [CaptureSource]
  public let applications: [CaptureSource]
  public let microphones: [CaptureSource]

  public init(
    displays: [CaptureSource],
    applications: [CaptureSource],
    microphones: [CaptureSource]
  ) {
    self.displays = displays
    self.applications = applications
    self.microphones = microphones
  }

  public var all: [CaptureSource] {
    displays + applications + microphones
  }
}
