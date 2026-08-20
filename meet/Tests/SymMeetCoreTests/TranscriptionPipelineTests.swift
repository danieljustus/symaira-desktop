import Foundation
import XCTest

@testable import SymMeetCore

final class TranscriptionPipelineTests: XCTestCase {
  private var root: URL!

  override func setUpWithError() throws {
    root = try makeTemporaryDirectory()
  }

  override func tearDownWithError() throws {
    try? FileManager.default.removeItem(at: root)
  }

  func testRunProducesOutcomeAndPersistsSegments() async throws {
    let engine = FakeTranscriptionEngine()
    let pipeline = TranscriptionPipeline(dataRoot: root)
    let sourceURL = try createDummyAudio()

    let outcome = try await pipeline.run(
      TranscriptionRequestOptions(
        sourceURL: sourceURL,
        title: "Test Meeting",
        language: "en",
        modelID: "tiny",
        modelVersion: "openai_whisper-tiny",
        engineID: "fake"
      ),
      engine: engine
    )

    XCTAssertEqual(outcome.status, .succeeded)
    XCTAssertEqual(outcome.segmentCount, 2)
    XCTAssertEqual(outcome.modelID, "tiny")
    XCTAssertEqual(outcome.engineID, "fake")
    XCTAssertNotNil(outcome.meetingID)
    XCTAssertNotNil(outcome.jobID)

    let segments = try await MeetingStore(dataRoot: root).rawSegments(
      meetingID: outcome.meetingID.uuidString.lowercased())
    XCTAssertEqual(segments.count, 2)
  }

  func testRetryDoesNotDuplicateFinalizedSegments() async throws {
    // Simulate an interruption *after* both segments were already finalized
    // and persisted but *before* the job reached `succeeded`: pause the fake
    // engine right before completion, then force cancellation. That leaves a
    // `cancelled` job (a valid retry source) with segments already on disk,
    // matching the acceptance criterion ("retrying after a simulated
    // interruption does not duplicate already-finalized segments") -- unlike
    // retrying an already-`succeeded` job, which the state machine correctly
    // rejects as not retryable.
    let engine = FakeTranscriptionEngine()
    await engine.pauseBeforeCompletion()
    let pipeline = TranscriptionPipeline(dataRoot: root)
    let sourceURL = try createDummyAudio()

    let task = Task {
      try await pipeline.run(
        TranscriptionRequestOptions(
          sourceURL: sourceURL,
          title: nil,
          language: nil,
          modelID: "tiny",
          modelVersion: "openai_whisper-tiny",
          engineID: "fake"
        ),
        engine: engine
      )
    }

    try await Task.sleep(for: .milliseconds(50))
    await pipeline.requestCancellation()
    await engine.resume()
    let first = try await task.value
    XCTAssertEqual(first.status, .cancelled)
    XCTAssertEqual(first.segmentCount, 2)

    let retried = try await pipeline.retry(
      meetingID: first.meetingID,
      engine: engine,
      modelID: "tiny",
      modelVersion: "openai_whisper-tiny"
    )

    XCTAssertEqual(retried.status, .succeeded)
    XCTAssertEqual(retried.attempt, 2)

    let segments = try await MeetingStore(dataRoot: root).rawSegments(
      meetingID: first.meetingID.uuidString.lowercased())
    XCTAssertEqual(
      segments.count, 2,
      "retry must not duplicate segments that were already finalized")
  }

  func testCancellationProducesTerminalState() async throws {
    // Real cooperative cancellation (pipeline.requestCancellation(), the same
    // seam the CLI's SIGINT handler uses), not the fake engine's own
    // `.cancel` outcome -- the two are unrelated mechanisms and racing both
    // at once is what made this test flaky.
    let engine = FakeTranscriptionEngine()
    await engine.pauseBeforeCompletion()
    let pipeline = TranscriptionPipeline(dataRoot: root)
    let sourceURL = try createDummyAudio()

    let task = Task {
      try await pipeline.run(
        TranscriptionRequestOptions(
          sourceURL: sourceURL,
          title: nil,
          language: nil,
          modelID: "tiny",
          modelVersion: "openai_whisper-tiny",
          engineID: "fake"
        ),
        engine: engine
      )
    }

    // Wait deterministically until the run has reached the paused point
    // (after all chunks are finalized, right before completion) before
    // requesting cancellation -- the fake engine signals it explicitly, so
    // there is no timing race.
    for await _ in engine.pausedSignal {
      break
    }
    await pipeline.requestCancellation()
    await engine.resume()
    let outcome = try await task.value

    XCTAssertTrue(
      outcome.status == .cancelled || outcome.status == .interrupted,
      "cancelled run must end in cancelled or interrupted, got \(outcome.status.rawValue)")
    XCTAssertNotEqual(outcome.status, .succeeded)
  }

  func testSecondRunCreatesDistinctMeetingWithSameSourceHash() async throws {
    let engine = FakeTranscriptionEngine()
    let pipeline = TranscriptionPipeline(dataRoot: root)
    let sourceURL = try createDummyAudio()

    let first = try await pipeline.run(
      TranscriptionRequestOptions(
        sourceURL: sourceURL,
        title: nil,
        language: nil,
        modelID: "tiny",
        modelVersion: "openai_whisper-tiny",
        engineID: "fake"
      ),
      engine: engine
    )

    let second = try await pipeline.run(
      TranscriptionRequestOptions(
        sourceURL: sourceURL,
        title: nil,
        language: nil,
        modelID: "tiny",
        modelVersion: "openai_whisper-tiny",
        engineID: "fake"
      ),
      engine: engine
    )

    XCTAssertNotEqual(
      first.meetingID, second.meetingID,
      "each run must create a distinct meeting")
    XCTAssertEqual(
      first.sourceHash, second.sourceHash,
      "same source file must produce the same source hash")
  }

  func testProgressReportsMeetingCreatedAndPhases() async throws {
    let engine = FakeTranscriptionEngine()
    let pipeline = TranscriptionPipeline(dataRoot: root)
    let sourceURL = try createDummyAudio()

    let progressCollector = ProgressCollector()
    let outcome = try await pipeline.run(
      TranscriptionRequestOptions(
        sourceURL: sourceURL,
        title: nil,
        language: nil,
        modelID: "tiny",
        modelVersion: "openai_whisper-tiny",
        engineID: "fake"
      ),
      engine: engine,
      onProgress: { progress in progressCollector.record(progress) }
    )

    XCTAssertEqual(outcome.status, .succeeded)
    let progressEvents = progressCollector.events
    guard case .meetingCreated = progressEvents.first else {
      XCTFail("First progress event must be .meetingCreated")
      return
    }
    let phases = progressEvents.compactMap { event -> TranscriptionPhase? in
      if case .phase(let p) = event { return p }
      return nil
    }
    XCTAssertTrue(phases.contains(.preparing))
    XCTAssertTrue(phases.contains(.transcribing))
  }

  func testEngineFailureRecordsFailureAndThrows() async throws {
    let engine = FakeTranscriptionEngine()
    await engine.setOutcome(.fail)
    let pipeline = TranscriptionPipeline(dataRoot: root)
    let sourceURL = try createDummyAudio()

    do {
      _ = try await pipeline.run(
        TranscriptionRequestOptions(
          sourceURL: sourceURL,
          title: nil,
          language: nil,
          modelID: "tiny",
          modelVersion: "openai_whisper-tiny",
          engineID: "fake"
        ),
        engine: engine
      )
      XCTFail("Expected engine failure")
    } catch PipelineError.engineFailed {
      // expected
    }
  }

  /// Writes a mono 16 kHz / 16-bit PCM WAV fixture spanning 1.5 seconds so the
  /// real AudioSampleReader (1-second output chunks) emits exactly two chunks,
  /// matching the fake engine's one-finalized-segment-per-chunk behavior.
  private func createDummyAudio() throws -> URL {
    let url = root.appending(path: "test-audio.wav")
    let sampleRate: UInt32 = 16_000
    let numSamples: UInt32 = sampleRate + sampleRate / 2  // 1.5 seconds
    let bytesPerSample: UInt32 = 2
    let dataSize = numSamples * bytesPerSample
    let riffSize = 36 + dataSize

    var data = Data()
    // RIFF header
    data.append(contentsOf: "RIFF".utf8)
    data.append(contentsOf: withUnsafeBytes(of: riffSize.littleEndian) { Array($0) })
    data.append(contentsOf: "WAVE".utf8)
    // fmt sub-chunk
    data.append(contentsOf: "fmt ".utf8)
    data.append(contentsOf: withUnsafeBytes(of: UInt32(16).littleEndian) { Array($0) })
    data.append(contentsOf: withUnsafeBytes(of: UInt16(1).littleEndian) { Array($0) })
    data.append(contentsOf: withUnsafeBytes(of: UInt16(1).littleEndian) { Array($0) })
    data.append(contentsOf: withUnsafeBytes(of: sampleRate.littleEndian) { Array($0) })
    let byteRate = sampleRate * bytesPerSample
    data.append(contentsOf: withUnsafeBytes(of: byteRate.littleEndian) { Array($0) })
    data.append(contentsOf: withUnsafeBytes(of: UInt16(2).littleEndian) { Array($0) })
    data.append(contentsOf: withUnsafeBytes(of: UInt16(16).littleEndian) { Array($0) })
    // data sub-chunk
    data.append(contentsOf: "data".utf8)
    data.append(contentsOf: withUnsafeBytes(of: dataSize.littleEndian) { Array($0) })
    for _ in 0..<numSamples {
      data.append(contentsOf: withUnsafeBytes(of: Int16(0).littleEndian) { Array($0) })
    }
    try data.write(to: url)
    return url
  }
}

private func makeTemporaryDirectory() throws -> URL {
  let directory = FileManager.default.temporaryDirectory.appending(
    path: "symmeet-pipeline-\(UUID().uuidString)")
  try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
  return directory
}

/// Thread-safe collector for the @Sendable onProgress closure in tests.
private final class ProgressCollector: @unchecked Sendable {
  private let lock = NSLock()
  private var storage: [PipelineProgress] = []

  func record(_ event: PipelineProgress) {
    lock.lock()
    storage.append(event)
    lock.unlock()
  }

  var events: [PipelineProgress] {
    lock.lock()
    defer { lock.unlock() }
    return storage
  }
}
