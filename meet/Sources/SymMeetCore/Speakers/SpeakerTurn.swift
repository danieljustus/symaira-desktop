import Foundation

/// The well-known speaker ID for the local microphone in native recordings.
/// Microphone tracks are mapped to this reserved slot so they are never
/// clustered with remote speakers by the diarization engine.
public enum LocalSpeaker {
  public static let reservedID = "speaker_local"
}

/// A single speaker turn produced by the diarization engine.
///
/// Speaker IDs are **meeting-local only** and are never persisted across
/// meetings.  Each turn carries provenance so issue #17 can build
/// speaker-correction and an event log on top of raw engine output.
public struct SpeakerTurn: Codable, Equatable, Sendable {
  public static let supportedSchemaVersion = 1

  public let schemaVersion: Int
  public let turnID: UUID
  public let speakerID: String
  public let startMS: Int
  public let endMS: Int
  public let confidence: Double
  public let isOverlapping: Bool
  public let provenance: TurnProvenance
  public var additionalFields: [String: JSONValue]

  public init(
    turnID: UUID = UUID(),
    speakerID: String,
    startMS: Int,
    endMS: Int,
    confidence: Double = 1.0,
    isOverlapping: Bool = false,
    provenance: TurnProvenance = .engine,
    additionalFields: [String: JSONValue] = [:]
  ) throws {
    guard startMS >= 0, endMS > startMS else {
      throw ContractError.invalidTimeRange(startMS: startMS, endMS: endMS)
    }
    guard confidence >= 0, confidence <= 1 else {
      throw DiarizationContractError.invalidConfidence(confidence)
    }
    schemaVersion = Self.supportedSchemaVersion
    self.turnID = turnID
    self.speakerID = speakerID
    self.startMS = startMS
    self.endMS = endMS
    self.confidence = confidence
    self.isOverlapping = isOverlapping
    self.provenance = provenance
    self.additionalFields = additionalFields
  }

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case schemaVersion = "schema_version"
    case turnID = "turn_id"
    case speakerID = "speaker_id"
    case startMS = "start_ms"
    case endMS = "end_ms"
    case confidence
    case isOverlapping = "is_overlapping"
    case provenance
  }

  public init(from decoder: Decoder) throws {
    let container = try decoder.container(keyedBy: CodingKeys.self)
    let schemaVersion = try container.decode(Int.self, forKey: .schemaVersion)
    guard schemaVersion == Self.supportedSchemaVersion else {
      throw ContractError.unsupportedSchemaVersion(schemaVersion)
    }
    let startMS = try container.decode(Int.self, forKey: .startMS)
    let endMS = try container.decode(Int.self, forKey: .endMS)
    guard startMS >= 0, endMS > startMS else {
      throw ContractError.invalidTimeRange(startMS: startMS, endMS: endMS)
    }

    self.schemaVersion = schemaVersion
    turnID = try container.decode(UUID.self, forKey: .turnID)
    speakerID = try container.decode(String.self, forKey: .speakerID)
    self.startMS = startMS
    self.endMS = endMS
    confidence = try container.decode(Double.self, forKey: .confidence)
    isOverlapping = try container.decode(Bool.self, forKey: .isOverlapping)
    provenance = try container.decode(TurnProvenance.self, forKey: .provenance)

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
    try container.encode(turnID, forKey: .turnID)
    try container.encode(speakerID, forKey: .speakerID)
    try container.encode(startMS, forKey: .startMS)
    try container.encode(endMS, forKey: .endMS)
    try container.encode(confidence, forKey: .confidence)
    try container.encode(isOverlapping, forKey: .isOverlapping)
    try container.encode(provenance, forKey: .provenance)

    var dynamicContainer = encoder.container(keyedBy: AnyCodingKey.self)
    for (key, value) in additionalFields where CodingKeys(rawValue: key) == nil {
      try dynamicContainer.encode(value, forKey: AnyCodingKey(key))
    }
  }
}

/// Provenance of a speaker turn.  `.engine` is the raw diarization output;
/// `.userCorrected` is set by issue #17's speaker-correction feature.
public enum TurnProvenance: String, Codable, CaseIterable, Sendable {
  case engine
  case userCorrected = "user_corrected"
}

/// Errors specific to diarization contract validation.
public enum DiarizationContractError: Error, Equatable, LocalizedError, Sendable {
  case invalidConfidence(Double)
  case unknownSpeakerID(String)

  public var errorDescription: String? {
    switch self {
    case .invalidConfidence(let value):
      "Diarization confidence must be in [0, 1], got \(value)."
    case .unknownSpeakerID(let id):
      "The speaker ID '\(id)' is not recognized in this meeting."
    }
  }
}
