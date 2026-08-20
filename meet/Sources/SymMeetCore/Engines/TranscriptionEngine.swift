import Foundation

public protocol TranscriptionEngine: Actor {
  var engineID: String { get }
  var capabilities: EngineCapabilities { get }

  func transcribe(
    _ request: TranscriptionRequest
  ) -> AsyncThrowingStream<TranscriptionEvent, Error>
}
