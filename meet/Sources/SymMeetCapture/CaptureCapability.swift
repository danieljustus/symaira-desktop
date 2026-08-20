import Foundation

/// The authorization status for a macOS capture capability.
public enum CaptureAuthorizationStatus: String, Codable, Equatable, Sendable {
  case authorized
  case denied
  case restricted
  case notDetermined

  /// The System Settings destination to direct the user when status is `denied`.
  public var systemSettingsURL: URL? {
    switch self {
    case .authorized, .notDetermined, .restricted:
      return nil
    case .denied:
      return URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy")
    }
  }
}

/// A named macOS capture capability with its current status.
public struct CaptureCapability: Codable, Equatable, Sendable {
  public let name: String
  public let status: CaptureAuthorizationStatus
  public let settingsURL: URL?

  public init(name: String, status: CaptureAuthorizationStatus) {
    self.name = name
    self.status = status
    self.settingsURL = status == .denied ? status.systemSettingsURL : nil
  }
}

/// A snapshot of all capture capabilities required by SymMeet.
public struct CaptureCapabilitySnapshot: Codable, Equatable, Sendable {
  public let microphone: CaptureCapability
  public let screenRecording: CaptureCapability
  /// True when both capabilities are fully authorized.
  public var allAuthorized: Bool {
    microphone.status == .authorized && screenRecording.status == .authorized
  }

  public init(microphone: CaptureCapability, screenRecording: CaptureCapability) {
    self.microphone = microphone
    self.screenRecording = screenRecording
  }
}
