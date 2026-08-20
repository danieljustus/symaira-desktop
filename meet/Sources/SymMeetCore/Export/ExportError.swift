import Foundation

/// Errors specific to rendering and writing meeting exports. Failures that
/// originate from loading the underlying artifact itself (a meeting that
/// truly does not exist, an unsafe path, a malformed manifest, ...) continue
/// to surface as ``StoreError`` from ``MeetingStore``; these cases cover only
/// the export-specific decisions layered on top.
public enum ExportError: Error, Equatable, LocalizedError, Sendable {
  /// The requested meeting is currently in local trash. Reuses the existing
  /// trash/restore concept from ``MeetingStore`` rather than inventing a new
  /// one -- see `symmeet meeting trash` / `symmeet meeting restore`.
  case meetingTrashed

  /// The requested meeting's transcription job has not reached
  /// ``MeetingJobState/completed``. The associated value is the best
  /// available description of the current state for the error message.
  case meetingIncomplete(jobState: String)

  /// `--segments edited` was requested explicitly, but no edited segments
  /// exist on disk for this meeting.
  case editedSegmentsUnavailable

  /// The output path already has a file and `--force` was not passed.
  case outputExists(String)

  /// The output path's parent directory does not exist.
  case invalidOutputPath(String)

  public var errorDescription: String? {
    switch self {
    case .meetingTrashed:
      return
        "This meeting is in local trash. Restore it with 'symmeet meeting restore', "
        + "or pass --include-trashed to export it anyway."
    case .meetingIncomplete(let jobState):
      return
        "This meeting's transcription job has not completed (current state: \(jobState)). "
        + "Pass --allow-incomplete to export the partial transcript anyway."
    case .editedSegmentsUnavailable:
      return
        "No edited segments exist for this meeting yet. Use --segments raw, "
        + "or edit the transcript first."
    case .outputExists(let path):
      return "Output file already exists: \(path). Pass --force to overwrite it."
    case .invalidOutputPath(let path):
      return "The output path's parent directory does not exist: \(path)."
    }
  }
}
