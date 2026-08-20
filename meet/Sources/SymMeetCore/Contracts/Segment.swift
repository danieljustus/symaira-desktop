import Foundation

public enum SegmentRevision: String, Codable, CaseIterable, Sendable {
  case engine
  case userCorrected = "user_corrected"
}

/// One timed span of transcript material. Engine output is immutable evidence;
/// `editedText` is an optional user-authored projection.
public struct Segment: Codable, Equatable, Sendable {
  public static let supportedSchemaVersion = 1

  public let schemaVersion: Int
  public let segmentID: UUID
  public let trackID: UUID
  public let speakerID: String
  public let startMS: Int
  public let endMS: Int
  public let engineText: String
  public let editedText: String?
  public let confidence: Double?
  public let revision: SegmentRevision
  public var additionalFields: [String: JSONValue]

  public init(
    segmentID: UUID,
    trackID: UUID,
    speakerID: String,
    startMS: Int,
    endMS: Int,
    engineText: String,
    editedText: String? = nil,
    confidence: Double? = nil,
    revision: SegmentRevision = .engine,
    additionalFields: [String: JSONValue] = [:]
  ) throws {
    try Self.validate(speakerID: speakerID, startMS: startMS, endMS: endMS)
    schemaVersion = Self.supportedSchemaVersion
    self.segmentID = segmentID
    self.trackID = trackID
    self.speakerID = speakerID
    self.startMS = startMS
    self.endMS = endMS
    self.engineText = engineText
    self.editedText = editedText
    self.confidence = confidence
    self.revision = revision
    self.additionalFields = additionalFields
  }

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case schemaVersion = "schema_version"
    case segmentID = "segment_id"
    case trackID = "track_id"
    case speakerID = "speaker_id"
    case startMS = "start_ms"
    case endMS = "end_ms"
    case engineText = "engine_text"
    case editedText = "edited_text"
    case confidence
    case revision
  }

  public init(from decoder: Decoder) throws {
    let container = try decoder.container(keyedBy: CodingKeys.self)
    let schemaVersion = try container.decode(Int.self, forKey: .schemaVersion)
    guard schemaVersion == Self.supportedSchemaVersion else {
      throw ContractError.unsupportedSchemaVersion(schemaVersion)
    }

    let speakerID = try container.decode(String.self, forKey: .speakerID)
    let startMS = try container.decode(Int.self, forKey: .startMS)
    let endMS = try container.decode(Int.self, forKey: .endMS)
    try Self.validate(speakerID: speakerID, startMS: startMS, endMS: endMS)

    self.schemaVersion = schemaVersion
    segmentID = try container.decode(UUID.self, forKey: .segmentID)
    trackID = try container.decode(UUID.self, forKey: .trackID)
    self.speakerID = speakerID
    self.startMS = startMS
    self.endMS = endMS
    engineText = try container.decode(String.self, forKey: .engineText)
    editedText = try container.decodeIfPresent(String.self, forKey: .editedText)
    confidence = try container.decodeIfPresent(Double.self, forKey: .confidence)
    revision = try container.decode(SegmentRevision.self, forKey: .revision)

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
    try container.encode(segmentID, forKey: .segmentID)
    try container.encode(trackID, forKey: .trackID)
    try container.encode(speakerID, forKey: .speakerID)
    try container.encode(startMS, forKey: .startMS)
    try container.encode(endMS, forKey: .endMS)
    try container.encode(engineText, forKey: .engineText)
    try container.encodeIfPresent(editedText, forKey: .editedText)
    try container.encodeIfPresent(confidence, forKey: .confidence)
    try container.encode(revision, forKey: .revision)

    var dynamicContainer = encoder.container(keyedBy: AnyCodingKey.self)
    for (key, value) in additionalFields where CodingKeys(rawValue: key) == nil {
      try dynamicContainer.encode(value, forKey: AnyCodingKey(key))
    }
  }

  private static func validate(speakerID: String, startMS: Int, endMS: Int) throws {
    guard speakerID.hasPrefix("speaker_") && speakerID.count > "speaker_".count else {
      throw ContractError.invalidIdentifier("speaker_id")
    }
    guard startMS >= 0, endMS > startMS else {
      throw ContractError.invalidTimeRange(startMS: startMS, endMS: endMS)
    }
  }
}
