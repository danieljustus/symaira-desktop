import Foundation

/// The stable, portable on-disk layout for one meeting.
public struct ArtifactLayout: Equatable, Sendable {
  public static let manifestFile = "manifest.json"
  public static let eventsFile = "events.jsonl"
  public static let rawSegmentsFile = "segments.raw.jsonl"
  public static let editedSegmentsFile = "segments.edited.jsonl"
  public static let transcriptFile = "transcript.md"
  public static let turnsRawFile = "turns.raw.jsonl"
  public static let turnsEditedFile = "turns.edited.jsonl"
  public static let alignmentFile = "alignment.json"
  public static let speakerEditsFile = "speaker_edits.jsonl"
  public static let speakerMapFile = "speaker_map.json"
  public static let pipelineStateFile = "pipeline_state.json"

  public let dataRoot: URL

  public init(dataRoot: URL) {
    self.dataRoot = dataRoot.standardizedFileURL
  }

  public var meetingsDirectory: URL {
    dataRoot.appending(path: "meetings", directoryHint: .isDirectory)
  }

  public var trashDirectory: URL {
    dataRoot.appending(path: "trash", directoryHint: .isDirectory)
  }

  public func meetingDirectory(_ meetingID: String) -> URL {
    meetingsDirectory.appending(path: meetingID, directoryHint: .isDirectory)
  }

  public func trashedMeetingDirectory(_ meetingID: String) -> URL {
    trashDirectory.appending(path: meetingID, directoryHint: .isDirectory)
  }

  public func manifestURL(in meetingDirectory: URL) -> URL {
    meetingDirectory.appending(path: Self.manifestFile, directoryHint: .notDirectory)
  }

  public func eventsURL(in meetingDirectory: URL) -> URL {
    meetingDirectory.appending(path: Self.eventsFile, directoryHint: .notDirectory)
  }

  public func rawSegmentsURL(in meetingDirectory: URL) -> URL {
    meetingDirectory.appending(path: Self.rawSegmentsFile, directoryHint: .notDirectory)
  }

  public func editedSegmentsURL(in meetingDirectory: URL) -> URL {
    meetingDirectory.appending(path: Self.editedSegmentsFile, directoryHint: .notDirectory)
  }

  public func transcriptURL(in meetingDirectory: URL) -> URL {
    meetingDirectory.appending(path: Self.transcriptFile, directoryHint: .notDirectory)
  }

  public func turnsRawURL(in meetingDirectory: URL) -> URL {
    meetingDirectory.appending(path: Self.turnsRawFile, directoryHint: .notDirectory)
  }

  public func turnsEditedURL(in meetingDirectory: URL) -> URL {
    meetingDirectory.appending(path: Self.turnsEditedFile, directoryHint: .notDirectory)
  }

  public func alignmentURL(in meetingDirectory: URL) -> URL {
    meetingDirectory.appending(path: Self.alignmentFile, directoryHint: .notDirectory)
  }

  public func speakerEditsURL(in meetingDirectory: URL) -> URL {
    meetingDirectory.appending(path: Self.speakerEditsFile, directoryHint: .notDirectory)
  }

  public func speakerMapURL(in meetingDirectory: URL) -> URL {
    meetingDirectory.appending(path: Self.speakerMapFile, directoryHint: .notDirectory)
  }

  public func pipelineStateURL(in meetingDirectory: URL) -> URL {
    meetingDirectory.appending(path: Self.pipelineStateFile, directoryHint: .notDirectory)
  }
}
