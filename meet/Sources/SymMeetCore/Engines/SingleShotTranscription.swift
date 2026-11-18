import Foundation

/// Runs an in-memory recording (16 kHz mono samples) through any `TranscriptionEngine` and
/// returns the plain text. Used by push-to-talk dictation, which has no files or timelines.
public enum SingleShotTranscription {
  public static let sampleRate = 16_000

  public enum Failure: Error, Equatable, Sendable {
    case noSamples
    case cancelled
  }

  /// Joins all finalized segments with single spaces. Engine errors propagate unchanged.
  public static func text(
    from samples: [Float],
    using engine: some TranscriptionEngine,
    modelID: String,
    language: String?
  ) async throws -> String {
    guard !samples.isEmpty else { throw Failure.noSamples }
    let durationMS = samples.count * 1_000 / sampleRate
    let chunk = AudioSampleChunk(samples: samples, startMS: 0, endMS: durationMS)
    let audio = AsyncThrowingStream<AudioSampleChunk, Error> { continuation in
      continuation.yield(chunk)
      continuation.finish()
    }
    let request = TranscriptionRequest(
      audio: audio,
      trackID: UUID(),
      modelID: modelID,
      language: language,
      sourceDurationMS: durationMS)

    var parts: [String] = []
    for try await event in await engine.transcribe(request) {
      switch event.type {
      case .finalizedSegment:
        if let text = event.segment?.text.trimmingCharacters(in: .whitespacesAndNewlines),
          !text.isEmpty
        {
          parts.append(text)
        }
      case .phase where event.phase == .cancelled:
        throw Failure.cancelled
      default:
        break
      }
    }
    return parts.joined(separator: " ")
  }
}
