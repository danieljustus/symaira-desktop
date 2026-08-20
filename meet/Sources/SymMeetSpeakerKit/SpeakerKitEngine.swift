import Foundation
// @preconcurrency import documented: upstream SpeakerKit is not yet strict-
// concurrency-clean; the adapter boundary contains all Sendable issues.
@preconcurrency import SpeakerKit
import SymMeetCore

/// The isolated SpeakerKit adapter.  No SpeakerKit type crosses this
/// module's public boundary; callers only observe SymMeetCore contracts.
///
/// Actor isolation prevents concurrent access to the upstream ``SpeakerKit``
/// instance, which is ``@unchecked Sendable`` upstream.
public actor SpeakerKitEngine: DiarizationEngine {
  public static let declaredCapabilities = EngineCapabilities(
    languages: [],
    supportsAutoDetection: true,
    supportsWordTimestamps: false,
    supportsSegmentTimestamps: false,
    supportsStreaming: false,
    supportsDiarization: true,
    requiredArchitectures: ["arm64"]
  )

  public let engineID = "speakerkit"
  public let capabilities = SpeakerKitEngine.declaredCapabilities

  private let modelID: String
  private let speakerKit: SpeakerKit
  private let trackAwareDiarizer = TrackAwareDiarizer()

  /// Creates a ``SpeakerKitEngine`` backed by a locally installed model.
  ///
  /// - Parameters:
  ///   - modelID: The model catalog identifier for the diarization model.
  ///   - modelStore: The local model store for verification.
  public init(modelID: String, modelStore: ModelStore = ModelStore()) async throws {
    let record: ModelRecord
    do {
      record = try await modelStore.verify(id: modelID)
    } catch {
      throw SpeakerKitDiarizationError.modelUnavailable
    }
    guard record.descriptor.engineID == "speakerkit" else {
      throw SpeakerKitDiarizationError.unsupportedModel
    }

    let root = await modelStore.root
    let modelFolder = root.appending(path: modelID, directoryHint: .isDirectory)
      .appending(path: "payload", directoryHint: .isDirectory)
    let config = PyannoteConfig(
      modelRepo: "argmaxinc/speakerkit-coreml",
      modelFolder: modelFolder.path,
      download: false,
      load: true,
      verbose: false
    )
    do {
      speakerKit = try await SpeakerKit(config)
    } catch {
      throw SpeakerKitDiarizationError.modelUnavailable
    }
    self.modelID = modelID
  }

  /// Internal init for testing with a pre-built upstream ``SpeakerKit``.
  internal init(modelID: String, speakerKit: SpeakerKit) {
    self.modelID = modelID
    self.speakerKit = speakerKit
  }

  public func diarize(
    _ request: DiarizationRequest
  ) async throws -> DiarizationOutput {
    guard !request.audioSamples.isEmpty else {
      throw SpeakerKitDiarizationError.emptyAudio
    }

    let numberOfSpeakers = request.numberOfSpeakers
    let options: PyannoteDiarizationOptions
    if let numberOfSpeakers {
      guard numberOfSpeakers >= 1 else {
        throw SpeakerKitDiarizationError.invalidSpeakerCount(numberOfSpeakers)
      }
      options = PyannoteDiarizationOptions(numberOfSpeakers: numberOfSpeakers)
    } else {
      options = PyannoteDiarizationOptions()
    }

    let upstreamResult = try await speakerKit.diarize(
      audioArray: request.audioSamples,
      options: options)

    let processed = trackAwareDiarizer.process(
      request,
      upstreamSegments: upstreamResult.segments)

    return DiarizationOutput(
      meetingID: request.meetingID,
      turns: processed.turns,
      speakerCount: processed.speakerCount,
      rttmLines: processed.rttmLines)
  }
}
