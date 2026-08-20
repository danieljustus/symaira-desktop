import Foundation

public enum SourceKind: String, Codable, CaseIterable, Sendable {
  case imported
  case liveCapture = "live_capture"
  case recovery
}

public enum AudioTrackKind: String, Codable, CaseIterable, Sendable {
  case microphone
  case system
  case original
}

public struct AudioTrack: Codable, Equatable, Sendable {
  public let trackID: UUID
  public let kind: AudioTrackKind
  public let relativePath: String

  public init(trackID: UUID, kind: AudioTrackKind, relativePath: String) {
    self.trackID = trackID
    self.kind = kind
    self.relativePath = relativePath
  }

  private enum CodingKeys: String, CodingKey {
    case trackID = "track_id"
    case kind
    case relativePath = "relative_path"
  }
}

public struct EngineProvenance: Codable, Equatable, Sendable {
  public let engineID: String
  public let modelID: String
  public let modelVersion: String

  public init(engineID: String, modelID: String, modelVersion: String) {
    self.engineID = engineID
    self.modelID = modelID
    self.modelVersion = modelVersion
  }

  private enum CodingKeys: String, CodingKey {
    case engineID = "engine_id"
    case modelID = "model_id"
    case modelVersion = "model_version"
  }
}

public enum MeetingJobState: String, Codable, CaseIterable, Sendable {
  case queued
  case processing
  case completed
  case failed
  case cancelled
}

public struct MeetingJob: Codable, Equatable, Sendable {
  public let jobID: UUID
  public let state: MeetingJobState
  public let engine: EngineProvenance?

  public init(jobID: UUID, state: MeetingJobState, engine: EngineProvenance? = nil) {
    self.jobID = jobID
    self.state = state
    self.engine = engine
  }

  private enum CodingKeys: String, CodingKey {
    case jobID = "job_id"
    case state
    case engine
  }
}

public enum ConsentStatus: String, Codable, CaseIterable, Sendable {
  case required
  case authorized
  case expired
  case revoked
}

public struct ConsentState: Codable, Equatable, Sendable {
  public let status: ConsentStatus
  public let authorizedAt: Date?
  public let expiresAt: Date?

  public init(status: ConsentStatus, authorizedAt: Date? = nil, expiresAt: Date? = nil) {
    self.status = status
    self.authorizedAt = authorizedAt
    self.expiresAt = expiresAt
  }

  private enum CodingKeys: String, CodingKey {
    case status
    case authorizedAt = "authorized_at"
    case expiresAt = "expires_at"
  }
}

public enum RetentionKind: String, Codable, CaseIterable, Sendable {
  case keep
  case deleteAfterDate = "delete_after_date"
  case deleteAfterExport = "delete_after_export"
}

public struct RetentionMetadata: Codable, Equatable, Sendable {
  public let policy: RetentionKind
  public let deleteAfter: Date?

  public init(policy: RetentionKind, deleteAfter: Date? = nil) {
    self.policy = policy
    self.deleteAfter = deleteAfter
  }

  private enum CodingKeys: String, CodingKey {
    case policy
    case deleteAfter = "delete_after"
  }
}

/// The portable v1 source-of-truth for a meeting artifact.
public struct MeetingManifest: Codable, Equatable, Sendable {
  public static let supportedSchemaVersion = 1

  public let schemaVersion: Int
  public let meetingID: UUID
  public let source: SourceKind
  public let createdAt: Date
  public let updatedAt: Date
  public let originalAsset: String?
  public let originalAssetMetadata: SourceAssetMetadata?
  public let audioTracks: [AudioTrack]
  public let language: String?
  public var job: MeetingJob?
  public let consent: ConsentState
  public let retention: RetentionMetadata
  public var additionalFields: [String: JSONValue]

  public init(
    meetingID: UUID,
    source: SourceKind,
    createdAt: Date,
    updatedAt: Date,
    originalAsset: String? = nil,
    originalAssetMetadata: SourceAssetMetadata? = nil,
    audioTracks: [AudioTrack] = [],
    language: String? = nil,
    job: MeetingJob? = nil,
    consent: ConsentState,
    retention: RetentionMetadata,
    additionalFields: [String: JSONValue] = [:]
  ) {
    schemaVersion = Self.supportedSchemaVersion
    self.meetingID = meetingID
    self.source = source
    self.createdAt = createdAt
    self.updatedAt = updatedAt
    self.originalAsset = originalAsset
    self.originalAssetMetadata = originalAssetMetadata
    self.audioTracks = audioTracks
    self.language = language
    self.job = job
    self.consent = consent
    self.retention = retention
    self.additionalFields = additionalFields
  }

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case schemaVersion = "schema_version"
    case meetingID = "meeting_id"
    case source
    case createdAt = "created_at"
    case updatedAt = "updated_at"
    case originalAsset = "original_asset"
    case originalAssetMetadata = "original_asset_metadata"
    case audioTracks = "audio_tracks"
    case language
    case job
    case consent
    case retention
  }

  public init(from decoder: Decoder) throws {
    let container = try decoder.container(keyedBy: CodingKeys.self)
    let schemaVersion = try container.decode(Int.self, forKey: .schemaVersion)
    guard schemaVersion == Self.supportedSchemaVersion else {
      throw ContractError.unsupportedSchemaVersion(schemaVersion)
    }

    self.schemaVersion = schemaVersion
    meetingID = try container.decode(UUID.self, forKey: .meetingID)
    source = try container.decode(SourceKind.self, forKey: .source)
    createdAt = try container.decode(Date.self, forKey: .createdAt)
    updatedAt = try container.decode(Date.self, forKey: .updatedAt)
    originalAsset = try container.decodeIfPresent(String.self, forKey: .originalAsset)
    originalAssetMetadata = try container.decodeIfPresent(
      SourceAssetMetadata.self, forKey: .originalAssetMetadata)
    audioTracks = try container.decodeIfPresent([AudioTrack].self, forKey: .audioTracks) ?? []
    language = try container.decodeIfPresent(String.self, forKey: .language)
    job = try container.decodeIfPresent(MeetingJob.self, forKey: .job)
    consent = try container.decode(ConsentState.self, forKey: .consent)
    retention = try container.decode(RetentionMetadata.self, forKey: .retention)

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
    try container.encode(meetingID, forKey: .meetingID)
    try container.encode(source, forKey: .source)
    try container.encode(createdAt, forKey: .createdAt)
    try container.encode(updatedAt, forKey: .updatedAt)
    try container.encodeIfPresent(originalAsset, forKey: .originalAsset)
    try container.encodeIfPresent(originalAssetMetadata, forKey: .originalAssetMetadata)
    try container.encode(audioTracks, forKey: .audioTracks)
    try container.encodeIfPresent(language, forKey: .language)
    try container.encodeIfPresent(job, forKey: .job)
    try container.encode(consent, forKey: .consent)
    try container.encode(retention, forKey: .retention)

    var dynamicContainer = encoder.container(keyedBy: AnyCodingKey.self)
    for (key, value) in additionalFields where CodingKeys(rawValue: key) == nil {
      try dynamicContainer.encode(value, forKey: AnyCodingKey(key))
    }
  }
}
