import Foundation

public enum MeetingLifecycleState: String, Codable, CaseIterable, Sendable {
  case created
  case recording
  case processing
  case completed
  case failed
  case deleted
}

public struct LifecycleTransition: Codable, Equatable, Sendable {
  public let from: MeetingLifecycleState
  public let to: MeetingLifecycleState

  public init(from: MeetingLifecycleState, to: MeetingLifecycleState) throws {
    guard Self.isAllowed(from: from, to: to) else {
      throw ContractError.invalidStateTransition(from: from, to: to)
    }
    self.from = from
    self.to = to
  }

  public static func isAllowed(from: MeetingLifecycleState, to: MeetingLifecycleState) -> Bool {
    switch (from, to) {
    case (.created, .recording), (.created, .processing), (.created, .deleted),
      (.recording, .processing), (.recording, .failed), (.recording, .deleted),
      (.processing, .completed), (.processing, .failed), (.processing, .deleted),
      (.failed, .processing), (.failed, .deleted), (.completed, .deleted):
      true
    default:
      false
    }
  }
}

public enum EventType: String, Codable, CaseIterable, Sendable {
  case lifecycleChanged = "lifecycle_changed"
  case segmentAdded = "segment_added"
  case transcriptEdited = "transcript_edited"
  case exportCompleted = "export_completed"
  case retentionRequested = "retention_requested"
}

/// Append-only JSONL entry. The event payload is extensible by design.
public struct EventEnvelope: Codable, Equatable, Sendable {
  public static let supportedSchemaVersion = 1

  public let schemaVersion: Int
  public let eventID: UUID
  public let meetingID: UUID
  public let type: EventType
  public let occurredAt: Date
  public let payload: JSONValue
  public var additionalFields: [String: JSONValue]

  public init(
    eventID: UUID,
    meetingID: UUID,
    type: EventType,
    occurredAt: Date,
    payload: JSONValue,
    additionalFields: [String: JSONValue] = [:]
  ) {
    schemaVersion = Self.supportedSchemaVersion
    self.eventID = eventID
    self.meetingID = meetingID
    self.type = type
    self.occurredAt = occurredAt
    self.payload = payload
    self.additionalFields = additionalFields
  }

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case schemaVersion = "schema_version"
    case eventID = "event_id"
    case meetingID = "meeting_id"
    case type
    case occurredAt = "occurred_at"
    case payload
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
    type = try container.decode(EventType.self, forKey: .type)
    occurredAt = try container.decode(Date.self, forKey: .occurredAt)
    payload = try container.decode(JSONValue.self, forKey: .payload)

    let dynamicContainer = try decoder.container(keyedBy: AnyCodingKey.self)
    let knownKeys = Set(CodingKeys.allCases.map(\.rawValue))
    additionalFields = try dynamicContainer.allKeys.reduce(into: [:]) { fields, key in
      guard !knownKeys.contains(key.stringValue) else { return }
      fields[key.stringValue] = try dynamicContainer.decode(JSONValue.self, forKey: key)
    }
  }

  public func encode(to encoder: Encoder) throws {
    var container = encoder.container(keyedBy: CodingKeys.self)
    try container.encode(Self.supportedSchemaVersion, forKey: .schemaVersion)
    try container.encode(eventID, forKey: .eventID)
    try container.encode(meetingID, forKey: .meetingID)
    try container.encode(type, forKey: .type)
    try container.encode(occurredAt, forKey: .occurredAt)
    try container.encode(payload, forKey: .payload)

    var dynamicContainer = encoder.container(keyedBy: AnyCodingKey.self)
    for (key, value) in additionalFields where CodingKeys(rawValue: key) == nil {
      try dynamicContainer.encode(value, forKey: AnyCodingKey(key))
    }
  }
}
