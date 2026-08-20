import Foundation

/// Errors raised while driving a file-to-transcript run through
/// ``TranscriptionPipeline``. Model resolution and installation errors are
/// surfaced by the CLI layer (which owns ``SymMeetCore/ModelStore``), not
/// here -- this type only covers failures intrinsic to running the pipeline
/// itself once a caller has already handed it a ready engine.
public enum PipelineError: Error, Equatable, LocalizedError, Sendable {
  /// The transcription engine's event stream ended (normally or abnormally)
  /// without ever reporting a ``TranscriptionEventType/completed`` event, and
  /// cancellation was not requested. This is treated as a failure rather than
  /// a silent partial success.
  case engineProducedNoCompletion
  /// The engine's event stream threw. The message is the underlying error's
  /// description, preserved for the job's failure history.
  case engineFailed(String)
  /// The requested meeting has no importable original asset on record, so a
  /// retry has nothing to re-transcribe.
  case missingOriginalAsset
  /// The meeting has no raw transcript segments available for diarization.
  case noSegmentsForDiarization
  /// The meeting has no raw transcript segments available for alignment.
  case noSegmentsForAlignment
  /// The requested meeting has no raw diarization turns.
  case noDiarizationTurns
  /// The requested meeting does not exist in the store.
  case meetingNotFound(String)

  public var errorDescription: String? {
    switch self {
    case .engineProducedNoCompletion:
      "The transcription engine ended without reporting completion."
    case .engineFailed(let message):
      "The transcription engine failed: \(message)."
    case .missingOriginalAsset:
      "This meeting has no imported original asset to retry from."
    case .noSegmentsForDiarization:
      "This meeting has no transcript segments to diarize."
    case .noSegmentsForAlignment:
      "This meeting has no transcript segments to align."
    case .noDiarizationTurns:
      "This meeting has no diarization turns to align with."
    case .meetingNotFound(let id):
      "Meeting '\(id)' was not found."
    }
  }
}
