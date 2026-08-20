import Foundation

public enum WhisperKitEngineError: Error, Equatable, LocalizedError, Sendable {
  case unsupportedModel
  case modelUnavailable
  case emptyAudio
  case transcriptionFailed

  public var errorDescription: String? {
    switch self {
    case .unsupportedModel:
      "The selected model is not a WhisperKit model."
    case .modelUnavailable:
      "The selected WhisperKit model is unavailable or cannot be loaded."
    case .emptyAudio:
      "The transcription input did not contain audio samples."
    case .transcriptionFailed:
      "WhisperKit could not transcribe the audio."
    }
  }
}
