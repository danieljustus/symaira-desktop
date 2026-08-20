import Foundation

public enum TranscriptionEventType: String, Codable, CaseIterable, Sendable {
  case phase
  case progress
  case partialSegment = "partial_segment"
  case finalizedSegment = "finalized_segment"
  case checkpoint
  case warning
  case completed
}

public enum TranscriptionPhase: String, Codable, CaseIterable, Sendable {
  case preparing
  case transcribing
  case exporting
  case cancelling
  case cancelled
  case completed
}

public struct SegmentDraft: Codable, Equatable, Sendable {
  public let segmentID: UUID
  public let trackID: UUID
  public let speakerID: String
  public let startMS: Int
  public let endMS: Int
  public let text: String

  public init(
    segmentID: UUID = UUID(),
    trackID: UUID,
    speakerID: String = "speaker_0",
    startMS: Int,
    endMS: Int,
    text: String
  ) {
    self.segmentID = segmentID
    self.trackID = trackID
    self.speakerID = speakerID
    self.startMS = startMS
    self.endMS = endMS
    self.text = text
  }

  private enum CodingKeys: String, CodingKey {
    case segmentID = "segment_id"
    case trackID = "track_id"
    case speakerID = "speaker_id"
    case startMS = "start_ms"
    case endMS = "end_ms"
    case text
  }
}

public struct TranscriptionCheckpoint: Codable, Equatable, Sendable {
  public let completedSourceTimeMS: Int
  public let engineID: String
  public let modelID: String

  public init(completedSourceTimeMS: Int, engineID: String, modelID: String) {
    self.completedSourceTimeMS = completedSourceTimeMS
    self.engineID = engineID
    self.modelID = modelID
  }

  private enum CodingKeys: String, CodingKey {
    case completedSourceTimeMS = "completed_source_time_ms"
    case engineID = "engine_id"
    case modelID = "model_id"
  }
}

public struct TranscriptionWarning: Codable, Equatable, Sendable {
  public let code: String
  public let message: String

  public init(code: String, message: String) {
    self.code = code
    self.message = message
  }
}

public struct TranscriptionCompletion: Codable, Equatable, Sendable {
  public let segmentCount: Int
  public let language: String?
  public let durationMS: Int

  public init(segmentCount: Int, language: String?, durationMS: Int) {
    self.segmentCount = segmentCount
    self.language = language
    self.durationMS = durationMS
  }

  private enum CodingKeys: String, CodingKey {
    case segmentCount = "segment_count"
    case language
    case durationMS = "duration_ms"
  }
}

public struct TranscriptionEvent: Codable, Equatable, Sendable {
  public let type: TranscriptionEventType
  public let phase: TranscriptionPhase?
  public let progress: Double?
  public let segment: SegmentDraft?
  public let checkpoint: TranscriptionCheckpoint?
  public let warning: TranscriptionWarning?
  public let completion: TranscriptionCompletion?

  public init(
    type: TranscriptionEventType,
    phase: TranscriptionPhase? = nil,
    progress: Double? = nil,
    segment: SegmentDraft? = nil,
    checkpoint: TranscriptionCheckpoint? = nil,
    warning: TranscriptionWarning? = nil,
    completion: TranscriptionCompletion? = nil
  ) {
    self.type = type
    self.phase = phase
    self.progress = progress
    self.segment = segment
    self.checkpoint = checkpoint
    self.warning = warning
    self.completion = completion
  }

  private enum CodingKeys: String, CodingKey {
    case type
    case phase
    case progress
    case segment
    case checkpoint
    case warning
    case completion
  }
}
