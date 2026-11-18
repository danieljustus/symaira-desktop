import Foundation
import SymDictationCore
import SymMeetCore
import SymMeetParakeet
import SymMeetWhisperKit

/// Bridges any `TranscriptionEngine` to the dictation core's `DictationTranscriber`.
struct EngineDictationTranscriber<Engine: TranscriptionEngine>: DictationTranscriber {
  let engine: Engine
  let modelID: String
  let language: String?

  func transcribe(samples: [Float]) async throws -> String {
    try await SingleShotTranscription.text(
      from: samples, using: engine, modelID: modelID, language: language)
  }
}

/// Loads the chosen engine, downloading its model on first use.
enum DictationEngineLoader {
  /// Dictation language hint. German is the product default; nil would let Parakeet detect.
  static let language = "de"
  static let whisperModelID = "large-v3-v20240930_626MB"

  static func load(
    _ choice: DictationEngineChoice,
    progress: @escaping @Sendable (Double) -> Void
  ) async throws -> any DictationTranscriber {
    switch choice {
    case .parakeet:
      let engine = try await ParakeetEngine.load(progress: progress)
      return EngineDictationTranscriber(
        engine: engine, modelID: ParakeetEngine.ultraModelID, language: language)
    case .whisperKit:
      let store = ModelStore()
      if (try? await store.verify(id: whisperModelID)) == nil {
        _ = try await WhisperKitModelInstaller(store: store).install(
          id: whisperModelID, progress: progress)
      }
      let engine = try await WhisperKitEngine(modelID: whisperModelID, modelStore: store)
      return EngineDictationTranscriber(engine: engine, modelID: whisperModelID, language: language)
    }
  }
}
