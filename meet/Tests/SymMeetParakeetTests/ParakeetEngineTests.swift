import FluidAudio
import SymMeetCore
import XCTest

@testable import SymMeetParakeet

private actor StubTranscriber: ParakeetTranscribing {
  private(set) var calls: [(count: Int, language: String?)] = []
  let reply: Result<String, Error>

  init(_ reply: Result<String, Error>) { self.reply = reply }

  func transcribe(samples: [Float], language: String?) async throws -> String {
    calls.append((samples.count, language))
    return try reply.get()
  }
}

private struct UpstreamFailure: Error {}

final class ParakeetEngineTests: XCTestCase {
  private func request(
    samples: [Float], language: String? = "de", durationMS: Int? = nil
  ) -> TranscriptionRequest {
    let chunk = AudioSampleChunk(samples: samples, startMS: 0, endMS: samples.count / 16)
    let audio = AsyncThrowingStream<AudioSampleChunk, Error> { continuation in
      if !samples.isEmpty { continuation.yield(chunk) }
      continuation.finish()
    }
    return TranscriptionRequest(
      audio: audio, trackID: UUID(), modelID: ParakeetEngine.ultraModelID, language: language,
      sourceDurationMS: durationMS ?? samples.count / 16)
  }

  private func collect(_ stream: AsyncThrowingStream<TranscriptionEvent, Error>) async throws
    -> [TranscriptionEvent]
  {
    var events: [TranscriptionEvent] = []
    for try await event in stream { events.append(event) }
    return events
  }

  func testCapabilitiesStayWithinCoreContract() {
    let capabilities = ParakeetEngine.declaredCapabilities
    XCTAssertTrue(capabilities.languages.contains("de"))
    XCTAssertEqual(capabilities.languages.count, 25)
    XCTAssertTrue(capabilities.supportsAutoDetection)
    XCTAssertFalse(capabilities.supportsDiarization)
    XCTAssertEqual(capabilities.requiredArchitectures, ["arm64"])
  }

  func testMapsRecordingToSingleFinalSegmentWithPhasesAndCompletion() async throws {
    let stub = StubTranscriber(.success("  Hallo Welt.  "))
    let engine = ParakeetEngine(modelID: "parakeet-ultra", transcriber: stub)
    let request = request(samples: Array(repeating: 0.2, count: 32_000))

    let events = try await collect(await engine.transcribe(request))

    XCTAssertEqual(events.first, TranscriptionEvent(type: .phase, phase: .preparing))
    XCTAssertTrue(events.contains(TranscriptionEvent(type: .phase, phase: .transcribing)))
    let segments = events.compactMap(\.segment)
    XCTAssertEqual(segments.count, 1)
    XCTAssertEqual(segments[0].text, "Hallo Welt.")
    XCTAssertEqual(segments[0].startMS, 0)
    XCTAssertEqual(segments[0].endMS, 2_000)
    XCTAssertEqual(segments[0].trackID, request.trackID)
    XCTAssertEqual(
      events.compactMap(\.checkpoint),
      [
        TranscriptionCheckpoint(
          completedSourceTimeMS: 2_000, engineID: "parakeet", modelID: "parakeet-ultra")
      ])
    XCTAssertEqual(
      events.last,
      TranscriptionEvent(
        type: .completed,
        completion: TranscriptionCompletion(segmentCount: 1, language: "de", durationMS: 2_000)))
  }

  func testPassesSamplesAndLanguageToUpstream() async throws {
    let stub = StubTranscriber(.success("x"))
    let engine = ParakeetEngine(modelID: "m", transcriber: stub)
    _ = try await collect(
      await engine.transcribe(request(samples: [0.1, 0.2, 0.3], language: "de")))
    let calls = await stub.calls
    XCTAssertEqual(calls.count, 1)
    XCTAssertEqual(calls[0].count, 3)
    XCTAssertEqual(calls[0].language, "de")
  }

  func testChunkedInputIsConcatenatedBeforeUpstreamCall() async throws {
    let stub = StubTranscriber(.success("x"))
    let engine = ParakeetEngine(modelID: "m", transcriber: stub)
    let audio = AsyncThrowingStream<AudioSampleChunk, Error> { continuation in
      continuation.yield(AudioSampleChunk(samples: [0.1, 0.2], startMS: 0, endMS: 1))
      continuation.yield(AudioSampleChunk(samples: [0.3], startMS: 1, endMS: 2))
      continuation.finish()
    }
    let req = TranscriptionRequest(
      audio: audio, trackID: UUID(), modelID: "m", language: nil, sourceDurationMS: 2)
    _ = try await collect(await engine.transcribe(req))
    let calls = await stub.calls
    XCTAssertEqual(calls.map(\.count), [3])
    XCTAssertNil(calls[0].language)
  }

  func testSilenceYieldsNoSegmentButStillCompletes() async throws {
    let engine = ParakeetEngine(modelID: "m", transcriber: StubTranscriber(.success("   ")))
    let events = try await collect(await engine.transcribe(request(samples: [0, 0, 0])))
    XCTAssertTrue(events.compactMap(\.segment).isEmpty)
    XCTAssertEqual(events.last?.completion?.segmentCount, 0)
  }

  func testEmptyAudioThrowsEmptyAudio() async {
    let engine = ParakeetEngine(modelID: "m", transcriber: StubTranscriber(.success("x")))
    do {
      _ = try await collect(await engine.transcribe(request(samples: [])))
      XCTFail("expected error")
    } catch {
      XCTAssertEqual(error as? ParakeetEngineError, .emptyAudio)
    }
  }

  func testUpstreamFailureIsMappedToTranscriptionFailedWithoutLeakingDetails() async {
    let engine = ParakeetEngine(
      modelID: "m", transcriber: StubTranscriber(.failure(UpstreamFailure())))
    do {
      _ = try await collect(await engine.transcribe(request(samples: [0.1])))
      XCTFail("expected error")
    } catch {
      XCTAssertEqual(error as? ParakeetEngineError, .transcriptionFailed)
    }
  }

  func testLanguageResolution() {
    XCTAssertEqual(ParakeetLanguage.resolve("de"), .german)
    XCTAssertEqual(ParakeetLanguage.resolve("de-DE"), .german)
    XCTAssertEqual(ParakeetLanguage.resolve("DE_at"), .german)
    XCTAssertEqual(ParakeetLanguage.resolve("en"), .english)
    XCTAssertNil(ParakeetLanguage.resolve(nil))
    XCTAssertNil(ParakeetLanguage.resolve(""))
    XCTAssertNil(ParakeetLanguage.resolve("tlh"))
  }

  func testEngineWorksThroughSingleShotHelper() async throws {
    let engine = ParakeetEngine(modelID: "m", transcriber: StubTranscriber(.success("Guten Tag")))
    let text = try await SingleShotTranscription.text(
      from: Array(repeating: 0.1, count: 16_000), using: engine, modelID: "m", language: "de")
    XCTAssertEqual(text, "Guten Tag")
  }
}
