import Foundation

public enum PrivacyError: Error, Equatable, LocalizedError, Sendable {
  case authorizationAlreadyUsed
  case authorizationExpired
  case authorizationNotForSession
  case invalidAuthorizationRecord
  case invalidInteractiveAttestation
  case localProcessingOnly
  case recordingNotActive

  public var errorDescription: String? {
    switch self {
    case .authorizationAlreadyUsed: "The recording authorization has already been used."
    case .authorizationExpired: "The recording authorization has expired."
    case .authorizationNotForSession: "The recording authorization is for a different session."
    case .invalidAuthorizationRecord: "A fresh interactive recording authorization is required."
    case .invalidInteractiveAttestation:
      "Interactive authorization did not include a valid operator attestation."
    case .localProcessingOnly: "This beta only permits local processing."
    case .recordingNotActive: "No authorized recording session is active."
    }
  }
}
