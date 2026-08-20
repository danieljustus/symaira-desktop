import Foundation

public enum ModelError: Error, Equatable, LocalizedError, Sendable {
  case invalidIdentifier
  case unknownModel
  case modelNotInstalled
  case corruptModel
  case incompatibleModel
  case inUse
  case invalidSource
  case operationFailed

  public var errorDescription: String? {
    switch self {
    case .invalidIdentifier: "The model identifier is invalid."
    case .unknownModel: "The requested model is not in the local catalog."
    case .modelNotInstalled: "The model is not installed."
    case .corruptModel: "The installed model is incomplete or corrupt."
    case .incompatibleModel: "The model is not compatible with this device."
    case .inUse: "The model is referenced by an active job."
    case .invalidSource: "The model source directory is invalid."
    case .operationFailed: "The local model operation failed."
    }
  }
}
