import Foundation

/// A meeting-local mapping of anonymous diarization speaker IDs to display
/// labels.  Labels are purely local to the meeting artifact; they never
/// reference Memory entity IDs or any external identity system.
///
/// ``SpeakerMap`` is a derived, replayable projection of the append-only
/// speaker edit event log.  It is never the source of truth -- the event log
/// in `speaker_edits.jsonl` is.
public struct SpeakerMap: Codable, Equatable, Sendable {
  public static let supportedSchemaVersion = 1

  public let schemaVersion: Int
  public let meetingID: UUID
  /// Display labels keyed by anonymous speaker ID.  Only speakers that have
  /// been explicitly labeled appear here.
  public let labels: [String: String]
  /// Maps a canonical speaker ID to the set of speaker IDs that were merged
  /// into it.  The canonical ID itself is always a key in ``labels``.
  public let mergedSpeakers: [String: [String]]
  /// Maps a segment ID to the new speaker ID assigned by a split edit.
  public let splitSegments: [UUID: String]
  /// The sequence number of the last edit that contributed to this map.
  public let lastEditSequence: Int

  public init(
    meetingID: UUID,
    labels: [String: String] = [:],
    mergedSpeakers: [String: [String]] = [:],
    splitSegments: [UUID: String] = [:],
    lastEditSequence: Int = 0
  ) {
    schemaVersion = Self.supportedSchemaVersion
    self.meetingID = meetingID
    self.labels = labels
    self.mergedSpeakers = mergedSpeakers
    self.splitSegments = splitSegments
    self.lastEditSequence = lastEditSequence
  }

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case schemaVersion = "schema_version"
    case meetingID = "meeting_id"
    case labels
    case mergedSpeakers = "merged_speakers"
    case splitSegments = "split_segments"
    case lastEditSequence = "last_edit_sequence"
  }

  public init(from decoder: Decoder) throws {
    let container = try decoder.container(keyedBy: CodingKeys.self)
    let schemaVersion = try container.decode(Int.self, forKey: .schemaVersion)
    guard schemaVersion == Self.supportedSchemaVersion else {
      throw ContractError.unsupportedSchemaVersion(schemaVersion)
    }
    self.schemaVersion = schemaVersion
    meetingID = try container.decode(UUID.self, forKey: .meetingID)
    labels = try container.decode([String: String].self, forKey: .labels)
    mergedSpeakers = try container.decode([String: [String]].self, forKey: .mergedSpeakers)
    splitSegments = try container.decode([UUID: String].self, forKey: .splitSegments)
    lastEditSequence = try container.decode(Int.self, forKey: .lastEditSequence)
  }

  public func encode(to encoder: Encoder) throws {
    var container = encoder.container(keyedBy: CodingKeys.self)
    try container.encode(Self.supportedSchemaVersion, forKey: .schemaVersion)
    try container.encode(meetingID, forKey: .meetingID)
    try container.encode(labels, forKey: .labels)
    try container.encode(mergedSpeakers, forKey: .mergedSpeakers)
    try container.encode(splitSegments, forKey: .splitSegments)
    try container.encode(lastEditSequence, forKey: .lastEditSequence)
  }
}

/// The kind of speaker correction edit.
public enum SpeakerEditKind: String, Codable, CaseIterable, Sendable {
  /// Assign a display label to an anonymous speaker ID.
  case label
  /// Merge one speaker into another (all turns from the source are
  /// reassigned to the target).
  case merge
  /// Split a specific segment away from its current speaker into a new
  /// anonymous speaker ID.
  case split
  /// Reset all edits, restoring the raw diarization state.
  case reset
}

/// An immutable, append-only speaker correction event.
///
/// Every edit is stored in the meeting's `speaker_edits.jsonl` event log and
/// can be replayed to reconstruct the derived ``SpeakerMap``.
public struct SpeakerEditEvent: Codable, Equatable, Sendable {
  public static let supportedSchemaVersion = 1

  public let schemaVersion: Int
  public let eventID: UUID
  public let meetingID: UUID
  public let kind: SpeakerEditKind
  /// The speaker ID affected by this edit (not present for `.reset`).
  public let speakerID: String?
  /// For `.label` edits: the new display label.
  public let label: String?
  /// For `.merge` edits: the target speaker ID to merge into.
  public let targetID: String?
  /// For `.split` edits: the segment ID to split out.
  public let segmentID: UUID?
  /// Monotonically increasing sequence number within the meeting.
  public let sequenceNumber: Int
  /// When the edit was created.
  public let occurredAt: Date

  public init(
    eventID: UUID = UUID(),
    meetingID: UUID,
    kind: SpeakerEditKind,
    speakerID: String? = nil,
    label: String? = nil,
    targetID: String? = nil,
    segmentID: UUID? = nil,
    sequenceNumber: Int,
    occurredAt: Date = Date()
  ) {
    schemaVersion = Self.supportedSchemaVersion
    self.eventID = eventID
    self.meetingID = meetingID
    self.kind = kind
    self.speakerID = speakerID
    self.label = label
    self.targetID = targetID
    self.segmentID = segmentID
    self.sequenceNumber = sequenceNumber
    self.occurredAt = occurredAt
  }

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case schemaVersion = "schema_version"
    case eventID = "event_id"
    case meetingID = "meeting_id"
    case kind
    case speakerID = "speaker_id"
    case label
    case targetID = "target_id"
    case segmentID = "segment_id"
    case sequenceNumber = "sequence_number"
    case occurredAt = "occurred_at"
  }

  public init(from decoder: Decoder) throws {
    let container = try decoder.container(keyedBy: CodingKeys.self)
    let schemaVersion = try container.decode(Int.self, forKey: .schemaVersion)
    guard schemaVersion == Self.supportedSchemaVersion else {
      throw ContractError.unsupportedSchemaVersion(schemaVersion)
    }
    self.schemaVersion = schemaVersion
    eventID = try container.decode(UUID.self, forKey: .eventID)
    meetingID = try container.decode(UUID.self, forKey: .meetingID)
    kind = try container.decode(SpeakerEditKind.self, forKey: .kind)
    speakerID = try container.decodeIfPresent(String.self, forKey: .speakerID)
    label = try container.decodeIfPresent(String.self, forKey: .label)
    targetID = try container.decodeIfPresent(String.self, forKey: .targetID)
    segmentID = try container.decodeIfPresent(UUID.self, forKey: .segmentID)
    sequenceNumber = try container.decode(Int.self, forKey: .sequenceNumber)
    occurredAt = try container.decode(Date.self, forKey: .occurredAt)
  }

  public func encode(to encoder: Encoder) throws {
    var container = encoder.container(keyedBy: CodingKeys.self)
    try container.encode(Self.supportedSchemaVersion, forKey: .schemaVersion)
    try container.encode(eventID, forKey: .eventID)
    try container.encode(meetingID, forKey: .meetingID)
    try container.encode(kind, forKey: .kind)
    try container.encodeIfPresent(speakerID, forKey: .speakerID)
    try container.encodeIfPresent(label, forKey: .label)
    try container.encodeIfPresent(targetID, forKey: .targetID)
    try container.encodeIfPresent(segmentID, forKey: .segmentID)
    try container.encode(sequenceNumber, forKey: .sequenceNumber)
    try container.encode(occurredAt, forKey: .occurredAt)
  }
}

/// Errors specific to speaker edit operations.
public enum SpeakerEditError: Error, Equatable, LocalizedError, Sendable {
  /// The referenced speaker ID does not exist in the raw diarization output.
  case speakerNotFound(String)
  /// The referenced segment ID does not exist.
  case segmentNotFound(UUID)
  /// The merge target is the same as the source.
  case mergeIntoSelf(String)
  /// The split segment is not assigned to the specified speaker.
  case segmentNotAssignedToSpeaker(segmentID: UUID, speakerID: String)
  /// The split segment's time range is empty or invalid.
  case invalidSplitRange
  /// A label is empty or exceeds the maximum length.
  case invalidLabel(String)
  /// The event log is corrupted or has gaps in sequence numbers.
  case corruptEventLog
  /// The operation would create a circular merge dependency.
  case circularMerge(from: String, to: String)

  public var errorDescription: String? {
    switch self {
    case .speakerNotFound(let id):
      "Speaker ID '\(id)' does not exist in the diarization output."
    case .segmentNotFound(let id):
      "Segment '\(id)' does not exist."
    case .mergeIntoSelf(let id):
      "Cannot merge speaker '\(id)' into itself."
    case .segmentNotAssignedToSpeaker(let segmentID, let speakerID):
      "Segment '\(segmentID)' is not assigned to speaker '\(speakerID)'."
    case .invalidSplitRange:
      "The segment's time range is invalid for splitting."
    case .invalidLabel(let label):
      "Invalid speaker label: '\(label)'."
    case .corruptEventLog:
      "The speaker edit event log is corrupted."
    case .circularMerge(let from, let to):
      "Merging '\(from)' into '\(to)' would create a circular dependency."
    }
  }
}
