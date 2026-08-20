import Foundation

public enum SpeakerKitDiarizationError: Error, Equatable, LocalizedError, Sendable {
  case modelUnavailable
  case unsupportedModel
  case emptyAudio
  case diarizationFailed
  case invalidSpeakerCount(Int)

  public var errorDescription: String? {
    switch self {
    case .modelUnavailable:
      "The diarization model is unavailable or cannot be loaded."
    case .unsupportedModel:
      "The selected model is not a SpeakerKit model."
    case .emptyAudio:
      "The diarization input did not contain audio samples."
    case .diarizationFailed:
      "SpeakerKit could not diarize the audio."
    case .invalidSpeakerCount(let count):
      "The speaker count \(count) is invalid; must be at least 1."
    }
  }
}
