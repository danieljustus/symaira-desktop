import Foundation
import SymMeetCapture
import SymMeetCore
import SymMeetWhisperKit

enum CLIExit: Int32 {
  case success = 0
  case runtimeFailure = 1
  case usage = 2
  case permissionDenied = 3
  case unsupported = 4
}

struct CLIError: Error {
  let exitCode: Int32
  let code: String
  let message: String
  let isJSON: Bool

  init(exitCode: Int32, code: String, message: String, isJSON: Bool = false) {
    self.exitCode = exitCode
    self.code = code
    self.message = message
    self.isJSON = isJSON
  }

  static func from(_ error: Error, isJSON: Bool = false) -> CLIError {
    if let error = error as? CLIError { return error }
    if let error = error as? ExportError {
      return CLIError(
        exitCode: CLIExit.usage.rawValue, code: errorCode(error),
        message: error.localizedDescription, isJSON: isJSON)
    }
    if let error = error as? ModelError {
      let code = errorCode(error)
      switch error {
      case .invalidIdentifier, .unknownModel:
        return CLIError(
          exitCode: CLIExit.usage.rawValue, code: code,
          message: error.localizedDescription, isJSON: isJSON)
      case .modelNotInstalled, .corruptModel, .incompatibleModel, .inUse, .invalidSource,
        .operationFailed:
        return CLIError(
          exitCode: CLIExit.runtimeFailure.rawValue, code: code,
          message: error.localizedDescription, isJSON: isJSON)
      }
    }
    if let error = error as? WhisperKitEngineError {
      return CLIError(
        exitCode: CLIExit.runtimeFailure.rawValue, code: "engine_error",
        message: error.localizedDescription, isJSON: isJSON)
    }
    if let error = error as? JobError {
      let code = errorCode(error)
      switch error {
      case .notFound, .invalidTransition:
        return CLIError(
          exitCode: CLIExit.usage.rawValue, code: code,
          message: error.localizedDescription, isJSON: isJSON)
      case .lockHeld, .lockNotOwned, .corruptRecord, .notInterrupted, .notRetryable,
        .operationFailed, .alreadyExists:
        return CLIError(
          exitCode: CLIExit.runtimeFailure.rawValue, code: code,
          message: error.localizedDescription, isJSON: isJSON)
      }
    }
    if let error = error as? PipelineError {
      return CLIError(
        exitCode: CLIExit.runtimeFailure.rawValue, code: "pipeline_error",
        message: error.localizedDescription, isJSON: isJSON)
    }
    if let error = error as? PrivacyError {
      let code = errorCode(error)
      switch error {
      case .invalidInteractiveAttestation, .invalidAuthorizationRecord,
        .authorizationAlreadyUsed, .authorizationExpired:
        return CLIError(
          exitCode: CLIExit.permissionDenied.rawValue, code: code,
          message: error.localizedDescription, isJSON: isJSON)
      case .authorizationNotForSession, .localProcessingOnly, .recordingNotActive:
        return CLIError(
          exitCode: CLIExit.runtimeFailure.rawValue, code: code,
          message: error.localizedDescription, isJSON: isJSON)
      }
    }
    if let error = error as? CaptureError {
      let code = errorCode(error)
      switch error {
      case .microphoneDenied, .screenRecordingDenied:
        return CLIError(
          exitCode: CLIExit.permissionDenied.rawValue, code: code,
          message: error.localizedDescription, isJSON: isJSON)
      case .microphoneRestricted, .screenRecordingUnavailable, .sourceNotFound,
        .osVersionUnsupported, .captureSessionAlreadyActive, .captureSessionNotActive,
        .noAuthorizationRecord, .bufferOverrun, .trackWriteFailed, .interruptedBySystem:
        return CLIError(
          exitCode: CLIExit.runtimeFailure.rawValue, code: code,
          message: error.localizedDescription, isJSON: isJSON)
      }
    }
    if let error = error as? AudioError {
      let code = errorCode(error)
      switch error {
      case .notLocalFile, .outsideApprovedPath, .missingFile, .directoryNotAllowed,
        .unsupportedContainer, .unsupportedCodec, .zeroLength, .invalidAudioFormat:
        return CLIError(
          exitCode: CLIExit.usage.rawValue, code: code,
          message: error.localizedDescription, isJSON: isJSON)
      case .missingAudioTrack, .protectedMedia, .exceedsByteLimit, .exceedsDurationLimit,
        .destinationExists, .cancelled, .operationFailed:
        return CLIError(
          exitCode: CLIExit.runtimeFailure.rawValue, code: code,
          message: error.localizedDescription, isJSON: isJSON)
      }
    }
    if let error = error as? ContractError {
      let code = errorCode(error)
      switch error {
      case .invalidIdentifier, .invalidStateTransition, .invalidTimeRange:
        return CLIError(
          exitCode: CLIExit.usage.rawValue, code: code,
          message: error.localizedDescription, isJSON: isJSON)
      case .unsupportedSchemaVersion:
        return CLIError(
          exitCode: CLIExit.runtimeFailure.rawValue, code: code,
          message: error.localizedDescription, isJSON: isJSON)
      }
    }
    guard let storeError = error as? StoreError else {
      return CLIError(
        exitCode: CLIExit.runtimeFailure.rawValue, code: "unknown_error",
        message: error.localizedDescription, isJSON: isJSON)
    }

    let storeCode = errorCode(storeError)
    switch storeError {
    case .invalidMeetingID, .invalidRelativePath:
      return CLIError(
        exitCode: CLIExit.usage.rawValue, code: storeCode,
        message: storeError.localizedDescription, isJSON: isJSON)
    case .unsafePath:
      return CLIError(
        exitCode: CLIExit.permissionDenied.rawValue, code: storeCode,
        message: "Access to the requested artifact was denied.", isJSON: isJSON)
    case .alreadyExists, .malformedArtifact, .missing, .operationFailed:
      return CLIError(
        exitCode: CLIExit.runtimeFailure.rawValue, code: storeCode,
        message: storeError.localizedDescription, isJSON: isJSON)
    }
  }

  static func unsupported(_ message: String) -> CLIError {
    CLIError(
      exitCode: CLIExit.unsupported.rawValue, code: "unsupported",
      message: message)
  }

  // MARK: - Helpers

  /// Converts an error's `String(describing:)` representation from camelCase
  /// to snake_case for use as a stable machine-readable error code.
  private static func errorCode(_ error: some Error) -> String {
    let full = String(describing: error)
    // Strip any module-qualified prefix (e.g. "SymMeetCore.PrivacyError.")
    let short = full.components(separatedBy: ".").last ?? full
    return camelToSnake(short)
  }

  private static func camelToSnake(_ input: String) -> String {
    var result = ""
    for char in input {
      if char.isUppercase {
        if !result.isEmpty { result += "_" }
        result += char.lowercased()
      } else {
        result += String(char)
      }
    }
    return result
  }
}
