import Foundation

public struct SourceAssetMetadata: Codable, Equatable, Sendable {
  public let container: String
  public let durationMS: Int
  public let channelCount: Int
  public let sampleRate: Double
  public let byteSize: Int64
  public let sha256: String

  public init(
    container: String,
    durationMS: Int,
    channelCount: Int,
    sampleRate: Double,
    byteSize: Int64,
    sha256: String
  ) {
    self.container = container
    self.durationMS = durationMS
    self.channelCount = channelCount
    self.sampleRate = sampleRate
    self.byteSize = byteSize
    self.sha256 = sha256
  }

  private enum CodingKeys: String, CodingKey {
    case container
    case durationMS = "duration_ms"
    case channelCount = "channel_count"
    case sampleRate = "sample_rate"
    case byteSize = "byte_size"
    case sha256
  }
}

public struct AudioAsset: Equatable, Sendable {
  public let sourceURL: URL
  public let metadata: SourceAssetMetadata
  public let audioTrackIndex: Int

  public init(sourceURL: URL, metadata: SourceAssetMetadata, audioTrackIndex: Int = 0) {
    self.sourceURL = sourceURL
    self.metadata = metadata
    self.audioTrackIndex = audioTrackIndex
  }

  public var fileExtension: String {
    sourceURL.pathExtension.lowercased()
  }
}

public struct AudioAssetImportResult: Equatable, Sendable {
  public let relativePath: String
  public let metadata: SourceAssetMetadata

  public init(relativePath: String, metadata: SourceAssetMetadata) {
    self.relativePath = relativePath
    self.metadata = metadata
  }
}
