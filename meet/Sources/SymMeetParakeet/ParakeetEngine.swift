import FluidAudio
import Foundation
import SymMeetCore

// MARK: - Upstream seam (internal, test-only injection point)

/// Decouples request mapping from the concrete FluidAudio manager so tests can stub the
/// upstream call. `language` is an ISO 639-1 code or `nil` for automatic detection.
internal protocol ParakeetTranscribing: Sendable {
  func transcribe(samples: [Float], language: String?) async throws -> String
}

/// Maps the Meet language code onto FluidAudio's script-filter language hint.
internal enum ParakeetLanguage {
  /// `"de"` and `"de-DE"` both resolve to German; unknown codes fall back to automatic.
  static func resolve(_ code: String?) -> Language? {
    guard let code, !code.isEmpty else { return nil }
    let primary = code.split(whereSeparator: { $0 == "-" || $0 == "_" }).first.map(String.init)
    return primary.flatMap { Language(rawValue: $0.lowercased()) }
  }
}

internal struct FluidAudioTranscriber: ParakeetTranscribing {
  private let manager: AsrManager

  init(manager: AsrManager) {
    self.manager = manager
  }

  func transcribe(samples: [Float], language: String?) async throws -> String {
    var state = TdtDecoderState.make(decoderLayers: await manager.decoderLayerCount)
    let result = try await manager.transcribe(
      samples, decoderState: &state, language: ParakeetLanguage.resolve(language))
    return result.text
  }
}

/// The isolated Parakeet adapter (FluidAudio, Core ML on the Neural Engine).
///
/// No FluidAudio type crosses this module's public boundary. The engine returns one finalized
/// segment per request: Parakeet produces text for the whole recording, which is what
/// dictation needs. Long inputs are chunked by FluidAudio itself.
public actor ParakeetEngine: TranscriptionEngine {
  /// Model identifier used in `TranscriptionRequest.modelID` and checkpoints. FluidAudio keeps
  /// the model files in its own cache, so this id is not a `ModelStore` entry.
  public static let ultraModelID = "parakeet-ultra"

  public static let declaredCapabilities = EngineCapabilities(
    languages: [
      "bg", "cs", "da", "de", "el", "en", "es", "et", "fi", "fr", "hr", "hu", "it",
      "lt", "lv", "mt", "nl", "pl", "pt", "ro", "ru", "sk", "sl", "sv", "uk",
    ],
    supportsAutoDetection: true,
    supportsWordTimestamps: false,
    supportsSegmentTimestamps: false,
    supportsStreaming: false,
    supportsDiarization: false,
    requiredArchitectures: ["arm64"]
  )

  public let engineID = "parakeet"
  public let capabilities = ParakeetEngine.declaredCapabilities

  private let modelID: String
  private let transcriber: any ParakeetTranscribing

  /// Downloads (first use only) and loads Parakeet Ultra. `progress` reports 0...1 on an
  /// unspecified queue while files are downloaded.
  public static func load(
    progress: (@Sendable (Double) -> Void)? = nil
  ) async throws -> ParakeetEngine {
    do {
      let models = try await AsrModels.downloadAndLoad(
        version: .ultra,
        progressHandler: progress.map { report in
          { @Sendable update in report(min(1, max(0, update.fractionCompleted))) }
        })
      let manager = AsrManager(config: .default)
      try await manager.loadModels(models)
      return ParakeetEngine(
        modelID: ultraModelID, transcriber: FluidAudioTranscriber(manager: manager))
    } catch {
      throw ParakeetEngineError.modelUnavailable
    }
  }

  /// Test seam: builds the engine around a stubbed upstream transcriber.
  internal init(modelID: String, transcriber: any ParakeetTranscribing) {
    self.modelID = modelID
    self.transcriber = transcriber
  }

  public func transcribe(
    _ request: TranscriptionRequest
  ) -> AsyncThrowingStream<TranscriptionEvent, Error> {
    AsyncThrowingStream { continuation in
      let task = Task { [weak self] in
        guard let self else {
          continuation.finish(throwing: ParakeetEngineError.transcriptionFailed)
          return
        }
        do {
          try await self.run(request, continuation: continuation)
          continuation.finish()
        } catch is CancellationError {
          continuation.yield(TranscriptionEvent(type: .phase, phase: .cancelled))
          continuation.finish()
        } catch let error as ParakeetEngineError {
          continuation.finish(throwing: error)
        } catch {
          continuation.finish(throwing: ParakeetEngineError.transcriptionFailed)
        }
      }
      continuation.onTermination = { _ in task.cancel() }
    }
  }

  private func run(
    _ request: TranscriptionRequest,
    continuation: AsyncThrowingStream<TranscriptionEvent, Error>.Continuation
  ) async throws {
    continuation.yield(TranscriptionEvent(type: .phase, phase: .preparing))

    var samples: [Float] = []
    samples.reserveCapacity(max(0, request.sourceDurationMS * 16))
    for try await chunk in request.audio {
      try Task.checkCancellation()
      samples.append(contentsOf: chunk.samples)
    }
    guard !samples.isEmpty else { throw ParakeetEngineError.emptyAudio }

    continuation.yield(TranscriptionEvent(type: .phase, phase: .transcribing))
    let text = try await transcriber.transcribe(samples: samples, language: request.language)
      .trimmingCharacters(in: .whitespacesAndNewlines)
    try Task.checkCancellation()

    let durationMS =
      request.sourceDurationMS > 0
      ? request.sourceDurationMS : samples.count * 1_000 / 16_000
    var segmentCount = 0
    if !text.isEmpty {
      segmentCount = 1
      continuation.yield(
        TranscriptionEvent(
          type: .finalizedSegment,
          segment: SegmentDraft(
            trackID: request.trackID, startMS: 0, endMS: durationMS, text: text)))
    }
    continuation.yield(TranscriptionEvent(type: .progress, progress: 1))
    continuation.yield(
      TranscriptionEvent(
        type: .checkpoint,
        checkpoint: TranscriptionCheckpoint(
          completedSourceTimeMS: durationMS, engineID: engineID, modelID: modelID)))
    continuation.yield(
      TranscriptionEvent(
        type: .completed,
        completion: TranscriptionCompletion(
          segmentCount: segmentCount, language: request.language, durationMS: durationMS)))
  }
}
