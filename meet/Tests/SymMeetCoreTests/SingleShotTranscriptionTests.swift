import XCTest

@testable import SymMeetCore

private actor ScriptedEngine: TranscriptionEngine {
  nonisolated let engineID = "scripted"
  nonisolated let capabilities = EngineCapabilities(
    languages: ["de"], supportsAutoDetection: false, supportsWordTimestamps: false,
    supportsSegmentTimestamps: false, supportsStreaming: false, supportsDiarization: false,
    requiredArchitectures: ["arm64"])
  private let events: [TranscriptionEvent]
  private let failure: (any Error)?
  private(set) var lastRequest: (modelID: String, language: String?, durationMS: Int)?

  init(events: [TranscriptionEvent], failure: (any Error)? = nil) {
    self.events = events
    self.failure = failure
  }

  func transcribe(_ request: TranscriptionRequest) -> AsyncThrowingStream<TranscriptionEvent, Error>
  {
    lastRequest = (request.modelID, request.language, request.sourceDurationMS)
    let events = events
    let failure = failure
    return AsyncThrowingStream { continuation in
      for event in events { continuation.yield(event) }
      continuation.finish(throwing: failure)
    }
  }
}

private func segment(_ text: String) -> TranscriptionEvent {
  TranscriptionEvent(
    type: .finalizedSegment,
    segment: SegmentDraft(trackID: UUID(), startMS: 0, endMS: 1, text: text))
}

final class SingleShotTranscriptionTests: XCTestCase {
  private let samples = [Float](repeating: 0.1, count: 16_000)

  func testJoinsFinalizedSegmentsAndIgnoresOtherEvents() async throws {
    let engine = ScriptedEngine(events: [
      TranscriptionEvent(type: .phase, phase: .preparing),
      segment(" Hallo "),
      TranscriptionEvent(type: .partialSegment, segment: nil),
      segment(""),
      segment("Welt"),
      TranscriptionEvent(type: .progress, progress: 1),
    ])
    let text = try await SingleShotTranscription.text(
      from: samples, using: engine, modelID: "m", language: "de")
    XCTAssertEqual(text, "Hallo Welt")
  }

  func testRequestCarriesModelLanguageAndDuration() async throws {
    let engine = ScriptedEngine(events: [])
    _ = try await SingleShotTranscription.text(
      from: samples, using: engine, modelID: "model-x", language: "de")
    let request = await engine.lastRequest
    XCTAssertEqual(request?.modelID, "model-x")
    XCTAssertEqual(request?.language, "de")
    XCTAssertEqual(request?.durationMS, 1_000)
  }

  func testEmptySamplesThrow() async {
    let engine = ScriptedEngine(events: [])
    do {
      _ = try await SingleShotTranscription.text(
        from: [], using: engine, modelID: "m", language: nil)
      XCTFail("expected error")
    } catch {
      XCTAssertEqual(error as? SingleShotTranscription.Failure, .noSamples)
    }
  }

  func testEngineErrorPropagates() async {
    struct Boom: Error, Equatable {}
    let engine = ScriptedEngine(events: [segment("x")], failure: Boom())
    do {
      _ = try await SingleShotTranscription.text(
        from: samples, using: engine, modelID: "m", language: nil)
      XCTFail("expected error")
    } catch {
      XCTAssertTrue(error is Boom)
    }
  }

  func testCancelledPhaseThrowsCancelled() async {
    let engine = ScriptedEngine(events: [TranscriptionEvent(type: .phase, phase: .cancelled)])
    do {
      _ = try await SingleShotTranscription.text(
        from: samples, using: engine, modelID: "m", language: nil)
      XCTFail("expected error")
    } catch {
      XCTAssertEqual(error as? SingleShotTranscription.Failure, .cancelled)
    }
  }
}
