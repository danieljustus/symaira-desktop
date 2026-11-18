import Foundation

public enum ParakeetEngineError: Error, Equatable, LocalizedError, Sendable {
  case modelUnavailable
  case emptyAudio
  case transcriptionFailed

  public var errorDescription: String? {
    switch self {
    case .modelUnavailable:
      "The Parakeet model could not be downloaded or loaded."
    case .emptyAudio:
      "The transcription input did not contain audio samples."
    case .transcriptionFailed:
      "Parakeet could not transcribe the audio."
    }
  }
}
