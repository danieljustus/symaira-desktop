import Foundation

/// The result of aligning diarization turns with transcript segments.
///
/// Alignment never modifies transcript text or timestamps -- it only assigns
/// speaker IDs to existing segments and records confidence/uncertainty.
/// Segments where no diarization turn covers the time range receive the
/// `unknownSpeakerID` sentinel rather than inheriting from the nearest turn.
public struct SpeakerAlignment: Codable, Equatable, Sendable {
  public static let supportedSchemaVersion = 1

  /// A sentinel speaker ID indicating the engine could not assign a speaker.
  public static let unknownSpeakerID = "speaker_unknown"

  public let schemaVersion: Int
  public let meetingID: UUID
  public let segmentID: UUID
  public let speakerID: String
  public let confidence: Double
  public let overlappingSpeakers: [String]

  public init(
    meetingID: UUID,
    segmentID: UUID,
    speakerID: String,
    confidence: Double = 1.0,
    overlappingSpeakers: [String] = []
  ) throws {
    guard confidence >= 0, confidence <= 1 else {
      throw DiarizationContractError.invalidConfidence(confidence)
    }
    schemaVersion = Self.supportedSchemaVersion
    self.meetingID = meetingID
    self.segmentID = segmentID
    self.speakerID = speakerID
    self.confidence = confidence
    self.overlappingSpeakers = overlappingSpeakers
  }

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case schemaVersion = "schema_version"
    case meetingID = "meeting_id"
    case segmentID = "segment_id"
    case speakerID = "speaker_id"
    case confidence
    case overlappingSpeakers = "overlapping_speakers"
  }

  public init(from decoder: Decoder) throws {
    let container = try decoder.container(keyedBy: CodingKeys.self)
    let schemaVersion = try container.decode(Int.self, forKey: .schemaVersion)
    guard schemaVersion == Self.supportedSchemaVersion else {
      throw ContractError.unsupportedSchemaVersion(schemaVersion)
    }
    self.schemaVersion = schemaVersion
    meetingID = try container.decode(UUID.self, forKey: .meetingID)
    segmentID = try container.decode(UUID.self, forKey: .segmentID)
    speakerID = try container.decode(String.self, forKey: .speakerID)
    confidence = try container.decode(Double.self, forKey: .confidence)
    overlappingSpeakers =
      try container.decodeIfPresent([String].self, forKey: .overlappingSpeakers) ?? []
  }

  public func encode(to encoder: Encoder) throws {
    var container = encoder.container(keyedBy: CodingKeys.self)
    try container.encode(Self.supportedSchemaVersion, forKey: .schemaVersion)
    try container.encode(meetingID, forKey: .meetingID)
    try container.encode(segmentID, forKey: .segmentID)
    try container.encode(speakerID, forKey: .speakerID)
    try container.encode(confidence, forKey: .confidence)
    try container.encode(overlappingSpeakers, forKey: .overlappingSpeakers)
  }
}

/// Pure alignment logic -- no I/O, no engine dependency.
///
/// Given an ordered list of ``Segment``s and an ordered list of
/// ``SpeakerTurn``s (both sorted by start time), each segment is assigned the
/// speaker turn with the largest temporal overlap.  When multiple speakers
/// overlap in the segment's time range, the primary speaker is the one with
/// the most overlap and the rest appear in ``SpeakerAlignment/overlappingSpeakers``.
/// When no turn overlaps at all, the alignment uses
/// ``SpeakerAlignment/unknownSpeakerID`` and zero confidence.
public enum SpeakerAligner {
  /// Aligns transcript segments with speaker turns.
  ///
  /// - Parameters:
  ///   - segments: Transcript segments sorted by start time.
  ///   - turns: Speaker turns sorted by start time.
  ///   - meetingID: The meeting these belong to.
  /// - Returns: One ``SpeakerAlignment`` per segment, in the same order.
  public static func align(
    segments: [Segment],
    turns: [SpeakerTurn],
    meetingID: UUID
  ) throws -> [SpeakerAlignment] {
    guard !turns.isEmpty else {
      return try segments.map { segment in
        try SpeakerAlignment(
          meetingID: meetingID,
          segmentID: segment.segmentID,
          speakerID: SpeakerAlignment.unknownSpeakerID,
          confidence: 0)
      }
    }

    return try segments.map { segment in
      try alignSingle(segment: segment, turns: turns, meetingID: meetingID)
    }
  }

  // MARK: - Private

  private static func alignSingle(
    segment: Segment,
    turns: [SpeakerTurn],
    meetingID: UUID
  ) throws -> SpeakerAlignment {
    var overlapBySpeaker: [String: Double] = [:]
    let segStart = Double(segment.startMS)
    let segEnd = Double(segment.endMS)
    let segDuration = segEnd - segStart

    for turn in turns {
      let overlapStart = max(segStart, Double(turn.startMS))
      let overlapEnd = min(segEnd, Double(turn.endMS))
      let overlap = max(0, overlapEnd - overlapStart)
      if overlap > 0 {
        overlapBySpeaker[turn.speakerID, default: 0] += overlap
      }
    }

    guard !overlapBySpeaker.isEmpty else {
      return try SpeakerAlignment(
        meetingID: meetingID,
        segmentID: segment.segmentID,
        speakerID: SpeakerAlignment.unknownSpeakerID,
        confidence: 0)
    }

    let sorted = overlapBySpeaker.sorted { $0.value > $1.value }
    let primary = sorted[0]
    let confidence = segDuration > 0 ? min(1, primary.value / segDuration) : 0
    let overlapping = sorted.dropFirst().map(\.key).sorted()

    return try SpeakerAlignment(
      meetingID: meetingID,
      segmentID: segment.segmentID,
      speakerID: primary.key,
      confidence: confidence,
      overlappingSpeakers: overlapping)
  }
}
