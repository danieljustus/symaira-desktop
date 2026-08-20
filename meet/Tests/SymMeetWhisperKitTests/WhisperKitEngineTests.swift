import Foundation
import SymMeetCore
import WhisperKit
import XCTest

@testable import SymMeetWhisperKit

/// A stub upstream transcriber that records the mapped request and returns a
/// canned result, so the adapter's request mapping and event shaping can be
/// tested without a model.
private actor StubTranscriber: WhisperKitTranscribing {
  private var receivedAudio: [Float] = []
  private var receivedOptions: DecodingOptions?
  private var result: [TranscriptionResult]
  private var error: Error?

  init(result: [TranscriptionResult] = [], error: Error? = nil) {
    self.result = result
    self.error = error
  }

  func transcribe(
    audioArray: [Float],
    decodeOptions: DecodingOptions,
    segmentCallback: @escaping @Sendable ([TranscriptionSegment]) -> Void
  ) async throws -> [TranscriptionResult] {
    receivedAudio = audioArray
    receivedOptions = decodeOptions
    if let error { throw error }
    // Emit the canned segments through the callback, as the real pipeline does.
    let segments = result.flatMap { $0.segments }
    if !segments.isEmpty {
      segmentCallback(segments)
    }
    return result
  }

  func capturedAudio() -> [Float] { receivedAudio }
  func capturedOptions() -> DecodingOptions? { receivedOptions }
}

private func makeRequest(
  samples: [Float] = [0.1, 0.2, 0.3],
  language: String? = "en",
  sourceDurationMS: Int = 1_000,
  chunkCount: Int = 1
) -> TranscriptionRequest {
  let stream = AsyncThrowingStream<AudioSampleChunk, Error> { continuation in
    for index in 0..<chunkCount {
      continuation.yield(
        AudioSampleChunk(
          samples: samples,
          startMS: index * 500,
          endMS: index * 500 + 500
        )
      )
    }
    continuation.finish()
  }
  return TranscriptionRequest(
    audio: stream,
    trackID: UUID(),
    modelID: "tiny",
    language: language,
    sourceDurationMS: sourceDurationMS
  )
}

private func makeResult(segments: [TranscriptionSegment], language: String = "en")
  -> TranscriptionResult
{
  TranscriptionResult(
    text: segments.map(\.text).joined(separator: " "),
    segments: segments,
    language: language,
    timings: TranscriptionTimings()
  )
}

final class WhisperKitEngineTests: XCTestCase {

  func testRequestMappingAggregatesChunksAndPassesLanguage() async throws {
    let transcriber = StubTranscriber(
      result: [makeResult(segments: [TranscriptionSegment(text: "hello")])])
    let engine = WhisperKitEngine(modelID: "tiny", transcriber: transcriber)

    let request = makeRequest(
      samples: [0.1, 0.2],
      language: "de",
      sourceDurationMS: 2_000,
      chunkCount: 2
    )
    var events: [TranscriptionEvent] = []
    for try await event in await engine.transcribe(request) {
      events.append(event)
    }

    // All samples across chunks are aggregated in order.
    let capturedAudio = await transcriber.capturedAudio()
    let capturedOptions = await transcriber.capturedOptions()
    let options = try XCTUnwrap(capturedOptions)
    XCTAssertEqual(capturedAudio, [0.1, 0.2, 0.1, 0.2])
    XCTAssertEqual(options.language, "de")
    XCTAssertFalse(options.detectLanguage)
    XCTAssertTrue(options.wordTimestamps)
    XCTAssertFalse(options.verbose)

    // Phase sequence: preparing → transcribing, then a .completed event.
    let phases = events.compactMap(\.phase)
    XCTAssertEqual(phases, [.preparing, .transcribing])
    XCTAssertEqual(events.last?.type, .completed)
    let completion = try XCTUnwrap(events.last?.completion)
    XCTAssertEqual(completion.segmentCount, 1)
    XCTAssertEqual(completion.language, "en")
    XCTAssertEqual(completion.durationMS, 2_000)
    let checkpoint = try XCTUnwrap(events.first(where: { $0.checkpoint != nil })?.checkpoint)
    XCTAssertEqual(checkpoint.engineID, "whisperkit")
    XCTAssertEqual(checkpoint.modelID, "tiny")
    XCTAssertEqual(checkpoint.completedSourceTimeMS, 2_000)
  }

  func testNilLanguageEnablesAutoDetection() async throws {
    let transcriber = StubTranscriber(
      result: [makeResult(segments: [TranscriptionSegment(text: "hi")])])
    let engine = WhisperKitEngine(modelID: "tiny", transcriber: transcriber)

    _ = try await collect(await engine.transcribe(makeRequest(language: nil)))

    let capturedOptions = await transcriber.capturedOptions()
    let options = try XCTUnwrap(capturedOptions)
    XCTAssertNil(options.language)
    XCTAssertTrue(options.detectLanguage)
  }

  func testSegmentCallbackShapesFinalizedSegmentsAndProgress() async throws {
    let transcriber = StubTranscriber(
      result: [
        makeResult(
          segments: [
            TranscriptionSegment(id: 0, start: 0.0, end: 1.5, text: " first "),
            TranscriptionSegment(id: 1, start: 1.5, end: 3.0, text: "second"),
          ])
      ])
    let engine = WhisperKitEngine(modelID: "tiny", transcriber: transcriber)

    let events = try await collect(
      await engine.transcribe(makeRequest(sourceDurationMS: 3_000)))

    let segments = events.compactMap(\.segment)
    XCTAssertEqual(segments.count, 2)
    XCTAssertEqual(segments[0].startMS, 0)
    XCTAssertEqual(segments[0].endMS, 1_500)
    XCTAssertEqual(segments[0].text, "first")  // trimmed
    XCTAssertEqual(segments[1].startMS, 1_500)
    XCTAssertEqual(segments[1].endMS, 3_000)

    // Progress events are clamped to [0, 1].
    let progress = events.compactMap(\.progress)
    XCTAssertEqual(progress, [0.5, 1.0])
  }

  func testEmptyAudioThrowsEmptyAudio() async {
    let transcriber = StubTranscriber(result: [])
    let engine = WhisperKitEngine(modelID: "tiny", transcriber: transcriber)

    let request = TranscriptionRequest(
      audio: AsyncThrowingStream { $0.finish() },
      trackID: UUID(),
      modelID: "tiny",
      language: nil,
      sourceDurationMS: 0
    )

    do {
      _ = try await collect(await engine.transcribe(request))
      XCTFail("Expected emptyAudio error")
    } catch let error as WhisperKitEngineError {
      XCTAssertEqual(error, .emptyAudio)
    } catch {
      XCTFail("Unexpected error: \(error)")
    }
  }

  func testEmptyResultsThrowTranscriptionFailed() async {
    let transcriber = StubTranscriber(result: [])
    let engine = WhisperKitEngine(modelID: "tiny", transcriber: transcriber)

    do {
      _ = try await collect(await engine.transcribe(makeRequest()))
      XCTFail("Expected transcriptionFailed error")
    } catch let error as WhisperKitEngineError {
      XCTAssertEqual(error, .transcriptionFailed)
    } catch {
      XCTFail("Unexpected error: \(error)")
    }
  }

  func testUpstreamErrorIsWrappedAsTranscriptionFailed() async {
    struct UpstreamError: Error {}
    let transcriber = StubTranscriber(error: UpstreamError())
    let engine = WhisperKitEngine(modelID: "tiny", transcriber: transcriber)

    do {
      _ = try await collect(await engine.transcribe(makeRequest()))
      XCTFail("Expected transcriptionFailed error")
    } catch let error as WhisperKitEngineError {
      XCTAssertEqual(error, .transcriptionFailed)
    } catch {
      XCTFail("Unexpected error: \(error)")
    }
  }

  func testCancellationEmitsCancelledPhase() async throws {
    let transcriber = StubTranscriber(error: CancellationError())
    let engine = WhisperKitEngine(modelID: "tiny", transcriber: transcriber)

    let events = try await collect(await engine.transcribe(makeRequest()))
    let phases = events.compactMap(\.phase)
    XCTAssertEqual(phases.last, .cancelled)
  }

  // MARK: - Helpers

  private func collect(
    _ stream: AsyncThrowingStream<TranscriptionEvent, Error>
  ) async throws -> [TranscriptionEvent] {
    var events: [TranscriptionEvent] = []
    for try await event in stream {
      events.append(event)
    }
    return events
  }
}
