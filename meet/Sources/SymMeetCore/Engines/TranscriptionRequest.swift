import Foundation

public struct TranscriptionRequest: Sendable {
  public let audio: AsyncThrowingStream<AudioSampleChunk, Error>
  public let trackID: UUID
  public let modelID: String
  public let language: String?
  public let sourceDurationMS: Int

  public init(
    audio: AsyncThrowingStream<AudioSampleChunk, Error>,
    trackID: UUID,
    modelID: String,
    language: String?,
    sourceDurationMS: Int
  ) {
    self.audio = audio
    self.trackID = trackID
    self.modelID = modelID
    self.language = language
    self.sourceDurationMS = sourceDurationMS
  }
}
