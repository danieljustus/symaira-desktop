import Foundation

/// The interoperable, deterministic formats a completed meeting can be
/// rendered into. See ``TranscriptRenderer`` for how each is produced.
public enum ExportFormat: String, CaseIterable, Equatable, Sendable {
  case markdown
  case txt
  case json
  case jsonl
  case srt
  case vtt
}

/// Which on-disk segment file a rendered export was sourced from. Segment
/// evidence is always immutable engine output (`segments.raw.jsonl`); an
/// `edited` overlay (`segments.edited.jsonl`) is optional and, when present,
/// is preferred by default -- see ``MeetingStore/editedSegments(meetingID:)``.
public enum ExportSegmentSource: String, CaseIterable, Equatable, Codable, Sendable {
  case raw
  case edited
}
