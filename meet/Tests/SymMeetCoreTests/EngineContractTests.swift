import Foundation
import XCTest

@testable import SymMeetCore

final class EngineContractTests: XCTestCase {
  func testCapabilityAndEventJSONUseSnakeCase() throws {
    let capabilities = EngineCapabilities(
      languages: ["en"],
      supportsAutoDetection: true,
      supportsWordTimestamps: true,
      supportsSegmentTimestamps: true,
      supportsStreaming: true,
      supportsDiarization: false,
      requiredArchitectures: ["arm64"])
    let event = TranscriptionEvent(
      type: .checkpoint,
      checkpoint: TranscriptionCheckpoint(
        completedSourceTimeMS: 1_000, engineID: "fake", modelID: "tiny"))

    let encoder = ContractCodec.encoder()
    let capabilityData = try encoder.encode(capabilities)
    let eventData = try encoder.encode(event)

    XCTAssertTrue(
      String(decoding: capabilityData, as: UTF8.self).contains("supports_auto_detection"))
    XCTAssertTrue(String(decoding: eventData, as: UTF8.self).contains("completed_source_time_ms"))
    XCTAssertEqual(
      try ContractCodec.decoder().decode(TranscriptionEvent.self, from: eventData), event)
  }

  func testFakeEngineEmitsBoundedProgressAndFinalizedSegments() async throws {
    let engine = FakeTranscriptionEngine()
    let trackID = UUID()
    let samples = AsyncThrowingStream<AudioSampleChunk, Error> { continuation in
      continuation.yield(AudioSampleChunk(samples: [0, 0], startMS: 0, endMS: 1_000))
      continuation.yield(AudioSampleChunk(samples: [0, 0], startMS: 1_000, endMS: 2_000))
      continuation.finish()
    }
    let request = TranscriptionRequest(
      audio: samples, trackID: trackID, modelID: "tiny", language: "en", sourceDurationMS: 2_000)

    var events: [TranscriptionEvent] = []
    for try await event in await engine.transcribe(request) {
      events.append(event)
    }

    XCTAssertEqual(events.filter { $0.type == .finalizedSegment }.count, 2)
    XCTAssertTrue(events.compactMap(\.progress).allSatisfy { (0...1).contains($0) })
    XCTAssertEqual(events.last?.type, .completed)
  }

  func testFakeEngineCanPauseAndResumeOrFail() async throws {
    let engine = FakeTranscriptionEngine()
    await engine.pauseBeforeCompletion()
    let samples = AsyncThrowingStream<AudioSampleChunk, Error> { continuation in
      continuation.yield(AudioSampleChunk(samples: [0], startMS: 0, endMS: 1))
      continuation.finish()
    }
    let request = TranscriptionRequest(
      audio: samples, trackID: UUID(), modelID: "tiny", language: nil, sourceDurationMS: 1)
    let task = Task {
      var events: [TranscriptionEvent] = []
      for try await event in await engine.transcribe(request) {
        events.append(event)
      }
      return events
    }
    try await Task.sleep(for: .milliseconds(10))
    XCTAssertFalse(task.isCancelled)
    await engine.resume()
    let completedEventType = try await task.value.last?.type
    XCTAssertEqual(completedEventType, .completed)

    await engine.setOutcome(.fail)
    let failingSamples = AsyncThrowingStream<AudioSampleChunk, Error> { continuation in
      continuation.yield(AudioSampleChunk(samples: [0], startMS: 0, endMS: 1))
      continuation.finish()
    }
    let failingRequest = TranscriptionRequest(
      audio: failingSamples, trackID: UUID(), modelID: "tiny", language: nil, sourceDurationMS: 1)
    do {
      for try await _ in await engine.transcribe(failingRequest) {}
      XCTFail("Expected fake engine failure")
    } catch let error as FakeEngineError {
      XCTAssertEqual(error, .requestedFailure)
    }
  }

  func testFakeEngineCancelsCooperativelyWithoutCompleting() async throws {
    let engine = FakeTranscriptionEngine()
    await engine.setOutcome(.cancel)
    let samples = AsyncThrowingStream<AudioSampleChunk, Error> { continuation in
      continuation.yield(AudioSampleChunk(samples: [0], startMS: 0, endMS: 1))
      continuation.finish()
    }
    let request = TranscriptionRequest(
      audio: samples, trackID: UUID(), modelID: "tiny", language: nil, sourceDurationMS: 1)

    var events: [TranscriptionEvent] = []
    for try await event in await engine.transcribe(request) {
      events.append(event)
    }

    XCTAssertEqual(events.last?.phase, .cancelled)
    XCTAssertFalse(events.contains { $0.type == .completed })
    XCTAssertFalse(events.contains { $0.type == .finalizedSegment })
  }
}
