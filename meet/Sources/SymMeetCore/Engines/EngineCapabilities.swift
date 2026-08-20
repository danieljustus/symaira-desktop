import Foundation

public struct EngineCapabilities: Codable, Equatable, Sendable {
  public let languages: [String]
  public let supportsAutoDetection: Bool
  public let supportsWordTimestamps: Bool
  public let supportsSegmentTimestamps: Bool
  public let supportsStreaming: Bool
  public let supportsDiarization: Bool
  public let requiredArchitectures: [String]

  public init(
    languages: [String],
    supportsAutoDetection: Bool,
    supportsWordTimestamps: Bool,
    supportsSegmentTimestamps: Bool,
    supportsStreaming: Bool,
    supportsDiarization: Bool,
    requiredArchitectures: [String]
  ) {
    self.languages = languages
    self.supportsAutoDetection = supportsAutoDetection
    self.supportsWordTimestamps = supportsWordTimestamps
    self.supportsSegmentTimestamps = supportsSegmentTimestamps
    self.supportsStreaming = supportsStreaming
    self.supportsDiarization = supportsDiarization
    self.requiredArchitectures = requiredArchitectures
  }

  private enum CodingKeys: String, CodingKey {
    case languages
    case supportsAutoDetection = "supports_auto_detection"
    case supportsWordTimestamps = "supports_word_timestamps"
    case supportsSegmentTimestamps = "supports_segment_timestamps"
    case supportsStreaming = "supports_streaming"
    case supportsDiarization = "supports_diarization"
    case requiredArchitectures = "required_architectures"
  }
}
