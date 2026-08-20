import Foundation

/// The recording source model for diarization.
///
/// Imported mixed recordings diarize the single normalized source.
/// Native recordings separate the microphone track (mapped to a local
/// anonymous speaker) from the system track (diarized for remote speakers).
public enum DiarizationSourceKind: String, Codable, CaseIterable, Sendable {
  /// Single mixed audio file (imported recording).
  case importedMixed = "imported_mixed"
  /// Native recording with separate microphone and system tracks.
  case nativeDualTrack = "native_dual_track"
}

/// Configuration for a diarization run.
public struct DiarizationRequest: Sendable {
  public let sourceKind: DiarizationSourceKind
  public let meetingID: UUID
  /// Audio samples at 16 kHz mono. For dual-track, this is the system track.
  public let audioSamples: [Float]
  /// The microphone track samples, if available (native dual-track only).
  public let microphoneSamples: [Float]?
  /// `nil` means let the engine estimate the speaker count.
  public let numberOfSpeakers: Int?
  /// Duration in milliseconds of the original audio.
  public let durationMS: Int

  public init(
    sourceKind: DiarizationSourceKind,
    meetingID: UUID,
    audioSamples: [Float],
    microphoneSamples: [Float]? = nil,
    numberOfSpeakers: Int? = nil,
    durationMS: Int
  ) {
    self.sourceKind = sourceKind
    self.meetingID = meetingID
    self.audioSamples = audioSamples
    self.microphoneSamples = microphoneSamples
    self.numberOfSpeakers = numberOfSpeakers
    self.durationMS = durationMS
  }
}

/// The result of a successful diarization run.
/// Named `DiarizationOutput` to avoid collision with upstream `SpeakerKit.DiarizationResult`.
public struct DiarizationOutput: Equatable, Sendable {
  public let meetingID: UUID
  public let turns: [SpeakerTurn]
  public let speakerCount: Int
  public let rttmLines: [String]

  public init(
    meetingID: UUID,
    turns: [SpeakerTurn],
    speakerCount: Int,
    rttmLines: [String] = []
  ) {
    self.meetingID = meetingID
    self.turns = turns
    self.speakerCount = speakerCount
    self.rttmLines = rttmLines
  }
}

/// The protocol every diarization engine must satisfy.
///
/// A diarization engine is always an actor for isolation of upstream SDK
/// objects.  Failure must never throw -- it must return a warning through
/// ``DiarizationWarning`` so that a successful transcript remains valid.
public protocol DiarizationEngine: Actor {
  var engineID: String { get }
  var capabilities: EngineCapabilities { get }

  func diarize(
    _ request: DiarizationRequest
  ) async throws -> DiarizationOutput
}

/// A non-fatal warning from the diarization engine.
public struct DiarizationWarning: Codable, Equatable, Sendable {
  public let code: String
  public let message: String

  public init(code: String, message: String) {
    self.code = code
    self.message = message
  }
}

/// The outcome of a diarization pipeline step: either success or a recorded
/// warning.  The meeting remains exportable even when `warning` is non-nil.
public struct DiarizationOutcome: Equatable, Sendable {
  public let result: DiarizationOutput?
  public let warning: DiarizationWarning?

  public init(result: DiarizationOutput?, warning: DiarizationWarning? = nil) {
    self.result = result
    self.warning = warning
  }
}
