import Foundation

public enum CaptureError: Error, Equatable, LocalizedError, Sendable {
  case microphoneDenied
  case microphoneRestricted
  case screenRecordingDenied
  case screenRecordingUnavailable
  case sourceNotFound(bundleID: String)
  case osVersionUnsupported
  case captureSessionAlreadyActive
  case captureSessionNotActive
  case noAuthorizationRecord
  case bufferOverrun
  case trackWriteFailed(reason: String)
  case interruptedBySystem(reason: String)

  public var errorDescription: String? {
    switch self {
    case .microphoneDenied:
      "Microphone access was denied. Open System Settings › Privacy & Security › Microphone to re-enable."
    case .microphoneRestricted:
      "Microphone access is restricted by a device management profile."
    case .screenRecordingDenied:
      "Screen Recording access was denied. Open System Settings › Privacy & Security › Screen Recording to re-enable."
    case .screenRecordingUnavailable:
      "Screen Recording is not available on this system configuration."
    case .sourceNotFound(let bundleID):
      "No running, capturable application was found with bundle ID '\(bundleID)'."
    case .osVersionUnsupported:
      "This feature requires macOS 15 or newer."
    case .captureSessionAlreadyActive:
      "A capture session is already active."
    case .captureSessionNotActive:
      "No capture session is currently active."
    case .noAuthorizationRecord:
      "A fresh interactive recording authorization is required before starting capture."
    case .bufferOverrun:
      "Audio buffer capacity was exceeded; samples were dropped. Reduce system load and try again."
    case .trackWriteFailed(let reason):
      "Writing the audio track failed: \(reason)"
    case .interruptedBySystem(let reason):
      "Capture was interrupted by the system: \(reason)"
    }
  }
}
