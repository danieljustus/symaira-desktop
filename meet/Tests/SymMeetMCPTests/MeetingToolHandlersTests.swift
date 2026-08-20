import Foundation
import SymMeetCore
import XCTest

@testable import SymMeetMCP

final class MeetingToolHandlersTests: XCTestCase {
  private var root: URL!

  override func setUpWithError() throws {
    root = try makeTemporaryDirectory()
  }

  override func tearDownWithError() throws {
    try? FileManager.default.removeItem(at: root)
  }

  // MARK: - meeting_list

  func testListEmptyStoreReturnsEmptyArrays() async throws {
    let store = MeetingStore(dataRoot: root)
    let result = try await MeetingListHandler(store: store).execute(args: [:])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(Set(json.keys), Set(["meetings", "diagnostics"]))
    XCTAssertEqual(try XCTUnwrap(json["meetings"] as? [Any]).count, 0)
    XCTAssertEqual(try XCTUnwrap(json["diagnostics"] as? [Any]).count, 0)
  }

  func testListReturnsMeetingSummariesAndDiagnostics() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(completedManifest(meetingID: meetingID))

    // A non-UUID directory is reported as a diagnostic, not a meeting.
    try FileManager.default.createDirectory(
      at: root.appending(path: "meetings/not-a-uuid"), withIntermediateDirectories: true)

    let result = try await MeetingListHandler(store: store).execute(args: [:])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)

    let meetings = try XCTUnwrap(json["meetings"] as? [[String: Any]])
    XCTAssertEqual(meetings.count, 1)
    XCTAssertEqual(
      Set(meetings[0].keys), Set(["meeting_id", "source", "created_at", "language", "job_state"]))
    XCTAssertEqual(meetings[0]["meeting_id"] as? String, meetingID.uuidString.lowercased())
    XCTAssertEqual(meetings[0]["source"] as? String, "imported")
    XCTAssertEqual(meetings[0]["language"] as? String, "en")
    XCTAssertEqual(meetings[0]["job_state"] as? String, "completed")

    let diagnostics = try XCTUnwrap(json["diagnostics"] as? [[String: Any]])
    XCTAssertEqual(diagnostics.count, 1)
    XCTAssertEqual(Set(diagnostics[0].keys), Set(["meeting_id", "code"]))
    XCTAssertEqual(diagnostics[0]["meeting_id"] as? String, "not-a-uuid")
    XCTAssertEqual(diagnostics[0]["code"] as? String, "invalid_meeting_directory")
  }

  func testListOmitsJobStateWhenMeetingHasNoJob() async throws {
    let store = MeetingStore(dataRoot: root)
    try await store.create(noJobManifest(meetingID: UUID()))

    let result = try await MeetingListHandler(store: store).execute(args: [:])

    let meetings = try XCTUnwrap(try jsonObject(result)["meetings"] as? [[String: Any]])
    XCTAssertEqual(meetings.count, 1)
    XCTAssertEqual(Set(meetings[0].keys), Set(["meeting_id", "source", "created_at", "language"]))
    XCTAssertEqual(meetings[0]["language"] as? String, "de")
  }

  // MARK: - meeting_get

  func testGetRequiresMeetingID() async throws {
    let store = MeetingStore(dataRoot: root)
    let result = try await MeetingGetHandler(store: store).execute(args: [:])

    XCTAssertTrue(result.isError)
    XCTAssertTrue(
      result.content.first?.text?.contains("Missing required parameter: meeting_id") ?? false)
  }

  func testGetUnknownMeetingThrows() async {
    let store = MeetingStore(dataRoot: root)
    let handler = MeetingGetHandler(store: store)

    await assertThrowsAsync {
      try await handler.execute(args: ["meeting_id": AnyCodable(UUID().uuidString)])
    }
  }

  func testGetReturnsManifestShapeWithoutSegments() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(completedManifest(meetingID: meetingID))

    let result = try await MeetingGetHandler(store: store).execute(
      args: ["meeting_id": AnyCodable(meetingID.uuidString)])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(
      Set(json.keys),
      Set([
        "meeting_id", "source", "created_at", "updated_at", "language", "job", "consent",
        "retention",
      ]))
    XCTAssertEqual(json["meeting_id"] as? String, meetingID.uuidString.lowercased())
    XCTAssertEqual(json["source"] as? String, "imported")
    XCTAssertEqual(json["language"] as? String, "en")

    let job = try XCTUnwrap(json["job"] as? [String: Any])
    XCTAssertEqual(Set(job.keys), Set(["job_id", "state"]))
    XCTAssertEqual(job["state"] as? String, "completed")

    let consent = try XCTUnwrap(json["consent"] as? [String: Any])
    XCTAssertEqual(Set(consent.keys), Set(["status"]))
    XCTAssertEqual(consent["status"] as? String, "authorized")

    let retention = try XCTUnwrap(json["retention"] as? [String: Any])
    XCTAssertEqual(Set(retention.keys), Set(["policy"]))
    XCTAssertEqual(retention["policy"] as? String, "keep")
  }

  func testGetOmitsJobWhenMeetingHasNoJob() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(noJobManifest(meetingID: meetingID))

    let result = try await MeetingGetHandler(store: store).execute(
      args: ["meeting_id": AnyCodable(meetingID.uuidString)])

    let json = try jsonObject(result)
    XCTAssertNil(json["job"])
  }

  func testGetWithSegmentsReturnsSegmentShapes() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(completedManifest(meetingID: meetingID))
    try await appendRawSegment(store, meetingID: meetingID, startMS: 0, endMS: 1000)
    try await appendRawSegment(store, meetingID: meetingID, startMS: 1000, endMS: 2000)

    let result = try await MeetingGetHandler(store: store).execute(
      args: [
        "meeting_id": AnyCodable(meetingID.uuidString),
        "include_segments": AnyCodable(true),
      ])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)

    let segments = try XCTUnwrap(json["segments"] as? [[String: Any]])
    XCTAssertEqual(segments.count, 2)
    XCTAssertEqual(
      Set(segments[0].keys),
      Set(["segment_id", "speaker_id", "start_ms", "end_ms", "text", "revision"]))
    XCTAssertEqual(segments[0]["speaker_id"] as? String, "speaker_0")
    XCTAssertEqual(segments[0]["start_ms"] as? Int, 0)
    XCTAssertEqual(segments[0]["end_ms"] as? Int, 1000)
    XCTAssertEqual(segments[0]["text"] as? String, "Hello world")
    XCTAssertEqual(segments[0]["revision"] as? String, "engine")
    XCTAssertEqual(json["segment_count"] as? Int, 2)
    XCTAssertEqual(json["segment_limit"] as? Int, 50)
  }

  func testGetRespectsSegmentLimit() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(completedManifest(meetingID: meetingID))
    try await appendRawSegment(store, meetingID: meetingID, startMS: 0, endMS: 1000)
    try await appendRawSegment(store, meetingID: meetingID, startMS: 1000, endMS: 2000)

    let result = try await MeetingGetHandler(store: store).execute(
      args: [
        "meeting_id": AnyCodable(meetingID.uuidString),
        "include_segments": AnyCodable(true),
        "segment_limit": AnyCodable(1),
      ])

    let json = try jsonObject(result)
    XCTAssertEqual(try XCTUnwrap(json["segments"] as? [Any]).count, 1)
    XCTAssertEqual(json["segment_count"] as? Int, 2)
    XCTAssertEqual(json["segment_limit"] as? Int, 1)
  }

  func testGetCapsSegmentLimitAtFiveHundred() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(completedManifest(meetingID: meetingID))
    try await appendRawSegment(store, meetingID: meetingID, startMS: 0, endMS: 1000)

    let result = try await MeetingGetHandler(store: store).execute(
      args: [
        "meeting_id": AnyCodable(meetingID.uuidString),
        "include_segments": AnyCodable(true),
        "segment_limit": AnyCodable(501),
      ])

    let json = try jsonObject(result)
    XCTAssertEqual(json["segment_limit"] as? Int, 500)
    XCTAssertEqual(json["segment_count"] as? Int, 1)
  }

  // MARK: - meeting_transcribe

  func testTranscribeRequiresFile() async throws {
    let store = MeetingStore(dataRoot: root)
    let handler = MeetingTranscribeHandler(store: store, dataRoot: root)
    let result = try await handler.execute(args: [:])

    XCTAssertTrue(result.isError)
    XCTAssertTrue(
      result.content.first?.text?.contains("Missing required parameter: file") ?? false)
  }

  func testTranscribeFileNotFound() async throws {
    let store = MeetingStore(dataRoot: root)
    let handler = MeetingTranscribeHandler(store: store, dataRoot: root)
    let result = try await handler.execute(
      args: ["file": AnyCodable(root.appending(path: "missing.wav").path)])

    XCTAssertTrue(result.isError)
    XCTAssertTrue(result.content.first?.text?.contains("File not found:") ?? false)
  }

  func testTranscribeModelNotInstalledFailsClosed() async throws {
    let store = MeetingStore(dataRoot: root)
    let modelStore = ModelStore(root: root.appending(path: "models"))
    let handler = MeetingTranscribeHandler(
      store: store, dataRoot: root, modelStore: modelStore)
    let audio = try createDummyAudio()

    let result = try await handler.execute(
      args: ["file": AnyCodable(audio.path), "model": AnyCodable("tiny")])

    XCTAssertTrue(result.isError)
    XCTAssertTrue(
      result.content.first?.text?.contains(
        "Model 'tiny' is not installed. Run: symmeet model install tiny")
        ?? false)
  }

  func testTranscribeSuccessReturnsSnakeCaseShape() async throws {
    let store = MeetingStore(dataRoot: root)
    let modelStore = try await makeInstalledModelStore()
    let engine = HandlerTestEngine()
    let handler = MeetingTranscribeHandler(
      store: store, dataRoot: root, modelStore: modelStore, engine: engine)
    let audio = try createDummyAudio()

    let result = try await handler.execute(
      args: [
        "file": AnyCodable(audio.path),
        "model": AnyCodable("tiny"),
        "language": AnyCodable("de"),
        "title": AnyCodable("Test Meeting"),
      ])

    XCTAssertFalse(result.isError, result.content.first?.text ?? "")
    let json = try jsonObject(result)
    XCTAssertEqual(
      Set(json.keys), Set(["meeting_id", "job_id", "state", "segment_count", "language"]))
    XCTAssertEqual(json["state"] as? String, "succeeded")
    XCTAssertEqual(json["segment_count"] as? Int, 1)
    XCTAssertEqual(json["language"] as? String, "de")

    // The run must have persisted a real meeting artifact with a completed job.
    let meetingID = try XCTUnwrap(json["meeting_id"] as? String)
    let stored = try await store.load(meetingID: meetingID)
    XCTAssertEqual(stored.job?.state.rawValue, "completed")
    let segments = try await store.rawSegments(meetingID: meetingID)
    XCTAssertEqual(segments.count, 1)
  }

  func testTranscribeDefaultStubEngineFailsGracefully() async throws {
    // The built-in stub engine produces no completion event, so the pipeline
    // fails; the handler must surface that as an error result, not a throw.
    let store = MeetingStore(dataRoot: root)
    let modelStore = try await makeInstalledModelStore()
    let handler = MeetingTranscribeHandler(
      store: store, dataRoot: root, modelStore: modelStore)
    let audio = try createDummyAudio()

    let result = try await handler.execute(
      args: ["file": AnyCodable(audio.path), "model": AnyCodable("tiny")])

    XCTAssertTrue(result.isError)
    XCTAssertTrue(result.content.first?.text?.contains("Transcription failed:") ?? false)
  }

  // MARK: - meeting_export

  func testExportRequiresMeetingID() async throws {
    let store = MeetingStore(dataRoot: root)
    let handler = MeetingExportHandler(store: store)
    let result = try await handler.execute(args: [:])

    XCTAssertTrue(result.isError)
    XCTAssertTrue(
      result.content.first?.text?.contains("Missing required parameter: meeting_id") ?? false)
  }

  func testExportInvalidFormat() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(completedManifest(meetingID: meetingID))
    let handler = MeetingExportHandler(store: store)

    let result = try await handler.execute(
      args: [
        "meeting_id": AnyCodable(meetingID.uuidString),
        "format": AnyCodable("docx"),
      ])

    XCTAssertTrue(result.isError)
    XCTAssertTrue(
      result.content.first?.text?.contains(
        "Invalid format. Supported: markdown, txt, json, jsonl, srt, vtt")
        ?? false)
  }

  func testExportUnknownMeetingThrows() async {
    let store = MeetingStore(dataRoot: root)
    let handler = MeetingExportHandler(store: store)

    await assertThrowsAsync {
      try await handler.execute(
        args: [
          "meeting_id": AnyCodable(UUID().uuidString),
          "format": AnyCodable("markdown"),
        ])
    }
  }

  func testExportRequiresCompletedJob() async throws {
    let store = MeetingStore(dataRoot: root)
    let handler = MeetingExportHandler(store: store)

    let queuedID = UUID()
    let now = Date()
    try await store.create(
      MeetingManifest(
        meetingID: queuedID,
        source: .imported,
        createdAt: now,
        updatedAt: now,
        job: MeetingJob(jobID: UUID(), state: .queued),
        consent: ConsentState(status: .authorized),
        retention: RetentionMetadata(policy: .keep)))
    let queued = try await handler.execute(
      args: [
        "meeting_id": AnyCodable(queuedID.uuidString),
        "format": AnyCodable("markdown"),
      ])
    XCTAssertTrue(queued.isError)
    XCTAssertTrue(
      queued.content.first?.text?.contains("not complete (state: queued)") ?? false)

    let noJobID = UUID()
    try await store.create(noJobManifest(meetingID: noJobID))
    let noJob = try await handler.execute(
      args: [
        "meeting_id": AnyCodable(noJobID.uuidString),
        "format": AnyCodable("markdown"),
      ])
    XCTAssertTrue(noJob.isError)
    XCTAssertTrue(
      noJob.content.first?.text?.contains("not complete (state: not_started)") ?? false)
  }

  func testExportDefaultPrefersRawAndReturnsShape() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(completedManifest(meetingID: meetingID))
    try await appendRawSegment(store, meetingID: meetingID)
    let handler = MeetingExportHandler(store: store)

    let result = try await handler.execute(
      args: [
        "meeting_id": AnyCodable(meetingID.uuidString),
        "format": AnyCodable("markdown"),
      ])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(
      Set(json.keys), Set(["meeting_id", "format", "segment_source", "segment_count", "content"]))
    XCTAssertEqual(json["meeting_id"] as? String, meetingID.uuidString.lowercased())
    XCTAssertEqual(json["format"] as? String, "markdown")
    XCTAssertEqual(json["segment_source"] as? String, "raw")
    XCTAssertEqual(json["segment_count"] as? Int, 1)
    let content = try XCTUnwrap(json["content"] as? String)
    XCTAssertTrue(content.contains("Hello world"))
  }

  func testExportExplicitRawSegments() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(completedManifest(meetingID: meetingID))
    try await appendRawSegment(store, meetingID: meetingID, text: "Only raw line")
    let handler = MeetingExportHandler(store: store)

    let result = try await handler.execute(
      args: [
        "meeting_id": AnyCodable(meetingID.uuidString),
        "format": AnyCodable("txt"),
        "segments": AnyCodable("raw"),
      ])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(json["segment_source"] as? String, "raw")
    XCTAssertTrue(try XCTUnwrap(json["content"] as? String).contains("Only raw line"))
  }

  func testExportEditedUnavailable() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(completedManifest(meetingID: meetingID))
    try await appendRawSegment(store, meetingID: meetingID)
    let handler = MeetingExportHandler(store: store)

    let result = try await handler.execute(
      args: [
        "meeting_id": AnyCodable(meetingID.uuidString),
        "format": AnyCodable("markdown"),
        "segments": AnyCodable("edited"),
      ])

    XCTAssertTrue(result.isError)
    XCTAssertTrue(
      result.content.first?.text?.contains("Edited segments are unavailable for this meeting.")
        ?? false)
  }

  func testExportInvalidSegmentsValue() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(completedManifest(meetingID: meetingID))
    let handler = MeetingExportHandler(store: store)

    let result = try await handler.execute(
      args: [
        "meeting_id": AnyCodable(meetingID.uuidString),
        "format": AnyCodable("markdown"),
        "segments": AnyCodable("bogus"),
      ])

    XCTAssertTrue(result.isError)
    XCTAssertTrue(
      result.content.first?.text?.contains("Invalid segment source. Supported: raw, edited.")
        ?? false)
  }

  func testExportPrefersEditedOverlayWhenPresent() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(completedManifest(meetingID: meetingID))
    try await appendRawSegment(store, meetingID: meetingID, text: "Raw line")

    // Hand-write the edited overlay exactly as the store would read it back.
    let layout = ArtifactLayout(dataRoot: root)
    let edited = try Segment(
      segmentID: UUID(), trackID: UUID(), speakerID: "speaker_0", startMS: 0, endMS: 1000,
      engineText: "Raw line", editedText: "Edited line", revision: .userCorrected)
    var line = try ContractCodec.encoder().encode(edited)
    line.append(0x0A)
    try line.write(
      to: layout.editedSegmentsURL(in: layout.meetingDirectory(meetingID.uuidString.lowercased())))

    let handler = MeetingExportHandler(store: store)
    let result = try await handler.execute(
      args: [
        "meeting_id": AnyCodable(meetingID.uuidString),
        "format": AnyCodable("markdown"),
      ])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(json["segment_source"] as? String, "edited")
    let content = try XCTUnwrap(json["content"] as? String)
    XCTAssertTrue(content.contains("Edited line"))
    XCTAssertFalse(content.contains("Raw line"))
  }

  func testExportJSONFormatRendersEnvelope() async throws {
    let store = MeetingStore(dataRoot: root)
    let meetingID = UUID()
    try await store.create(completedManifest(meetingID: meetingID))
    try await appendRawSegment(store, meetingID: meetingID)
    let handler = MeetingExportHandler(store: store)

    let result = try await handler.execute(
      args: [
        "meeting_id": AnyCodable(meetingID.uuidString),
        "format": AnyCodable("json"),
      ])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(json["format"] as? String, "json")
    XCTAssertEqual(json["segment_source"] as? String, "raw")

    let content = try XCTUnwrap(json["content"] as? String)
    let envelope = try XCTUnwrap(
      JSONSerialization.jsonObject(with: Data(content.utf8)) as? [String: Any])
    XCTAssertEqual(envelope["meeting_id"] as? String, meetingID.uuidString)
    XCTAssertEqual(envelope["segment_source"] as? String, "raw")
    XCTAssertEqual(envelope["segment_count"] as? Int, 1)
  }

  // MARK: - Fixtures

  private func completedManifest(meetingID: UUID) -> MeetingManifest {
    let now = Date()
    return MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: now,
      updatedAt: now,
      language: "en",
      job: MeetingJob(
        jobID: UUID(), state: .completed,
        engine: EngineProvenance(engineID: "whisperkit", modelID: "tiny", modelVersion: "v1")),
      consent: ConsentState(status: .authorized),
      retention: RetentionMetadata(policy: .keep))
  }

  private func noJobManifest(meetingID: UUID) -> MeetingManifest {
    let now = Date()
    return MeetingManifest(
      meetingID: meetingID,
      source: .liveCapture,
      createdAt: now,
      updatedAt: now,
      language: "de",
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .deleteAfterExport))
  }

  private func appendRawSegment(
    _ store: MeetingStore, meetingID: UUID, text: String = "Hello world",
    startMS: Int = 0, endMS: Int = 1000
  ) async throws {
    let segment = try Segment(
      segmentID: UUID(), trackID: UUID(), speakerID: "speaker_0",
      startMS: startMS, endMS: endMS, engineText: text)
    try await store.appendRawSegment(segment, meetingID: meetingID.uuidString.lowercased())
  }

  /// Installs the "tiny" model into a temp-dir model store so the transcribe
  /// handler's model gate passes deterministically without touching user data.
  private func makeInstalledModelStore() async throws -> ModelStore {
    let modelStore = ModelStore(root: root.appending(path: "models"))
    let payload = root.appending(path: "model-payload")
    try FileManager.default.createDirectory(
      at: payload.appending(path: "payload"), withIntermediateDirectories: true)
    _ = try await modelStore.publish("tiny", from: payload)
    return modelStore
  }

  /// Writes a mono 16 kHz / 16-bit PCM WAV fixture spanning 1.5 seconds, the
  /// same fixture shape the core pipeline tests use.
  private func createDummyAudio() throws -> URL {
    let url = root.appending(path: "test-audio.wav")
    let sampleRate: UInt32 = 16_000
    let numSamples: UInt32 = sampleRate + sampleRate / 2
    let bytesPerSample: UInt32 = 2
    let dataSize = numSamples * bytesPerSample
    let riffSize = 36 + dataSize

    var data = Data()
    data.append(contentsOf: "RIFF".utf8)
    data.append(contentsOf: withUnsafeBytes(of: riffSize.littleEndian) { Array($0) })
    data.append(contentsOf: "WAVE".utf8)
    data.append(contentsOf: "fmt ".utf8)
    data.append(contentsOf: withUnsafeBytes(of: UInt32(16).littleEndian) { Array($0) })
    data.append(contentsOf: withUnsafeBytes(of: UInt16(1).littleEndian) { Array($0) })
    data.append(contentsOf: withUnsafeBytes(of: UInt16(1).littleEndian) { Array($0) })
    data.append(contentsOf: withUnsafeBytes(of: sampleRate.littleEndian) { Array($0) })
    let byteRate = sampleRate * bytesPerSample
    data.append(contentsOf: withUnsafeBytes(of: byteRate.littleEndian) { Array($0) })
    data.append(contentsOf: withUnsafeBytes(of: UInt16(2).littleEndian) { Array($0) })
    data.append(contentsOf: withUnsafeBytes(of: UInt16(16).littleEndian) { Array($0) })
    data.append(contentsOf: "data".utf8)
    data.append(contentsOf: withUnsafeBytes(of: dataSize.littleEndian) { Array($0) })
    for _ in 0..<numSamples {
      data.append(contentsOf: withUnsafeBytes(of: Int16(0).littleEndian) { Array($0) })
    }
    try data.write(to: url)
    return url
  }
}

/// A deterministic test engine: one finalized segment plus a completion event,
/// never touching the request's audio chunks.
private actor HandlerTestEngine: TranscriptionEngine {
  nonisolated let engineID = "test"
  nonisolated let capabilities = EngineCapabilities(
    languages: ["en", "de"],
    supportsAutoDetection: true,
    supportsWordTimestamps: false,
    supportsSegmentTimestamps: true,
    supportsStreaming: true,
    supportsDiarization: false,
    requiredArchitectures: ["arm64", "x86_64"])

  func transcribe(
    _ request: TranscriptionRequest
  ) -> AsyncThrowingStream<TranscriptionEvent, Error> {
    AsyncThrowingStream { continuation in
      continuation.yield(
        TranscriptionEvent(
          type: .finalizedSegment,
          segment: SegmentDraft(
            trackID: request.trackID, speakerID: "speaker_0", startMS: 0, endMS: 1000,
            text: "Hello from the MCP test engine")))
      continuation.yield(
        TranscriptionEvent(
          type: .completed,
          completion: TranscriptionCompletion(
            segmentCount: 1, language: request.language, durationMS: request.sourceDurationMS)))
      continuation.finish()
    }
  }
}
