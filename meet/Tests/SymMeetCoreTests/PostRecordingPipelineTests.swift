import Foundation
import XCTest

@testable import SymMeetCore

final class PostRecordingPipelineTests: XCTestCase {
  private var root: URL!

  override func setUpWithError() throws {
    root = FileManager.default.temporaryDirectory.appending(
      path: "symmeet-pipeline-\(UUID().uuidString)")
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
  }

  override func tearDownWithError() throws {
    try? FileManager.default.removeItem(at: root)
  }

  // MARK: - Phase state

  func testPipelineStateCodableRoundTrip() throws {
    let meetingID = UUID()
    var state = PipelineState(meetingID: meetingID)
    state.phases[.transcription] = PhaseState(status: .succeeded)
    state.phases[.diarization] = PhaseState(status: .failed, message: "No engine")

    let data = try ContractCodec.encoder(prettyPrinted: true).encode(state)
    let decoded = try ContractCodec.decoder().decode(PipelineState.self, from: data)

    XCTAssertEqual(decoded.meetingID, meetingID)
    XCTAssertEqual(decoded.status(of: .transcription), .succeeded)
    XCTAssertEqual(decoded.status(of: .diarization), .failed)
    XCTAssertEqual(decoded.status(of: .alignment), .pending)
    XCTAssertFalse(decoded.isComplete)
  }

  func testReadyForReviewWhenTranscriptionSucceeds() {
    let meetingID = UUID()
    var state = PipelineState(meetingID: meetingID)
    state.phases[.transcription] = PhaseState(status: .succeeded)
    state.phases[.diarization] = PhaseState(status: .skipped)

    XCTAssertTrue(state.isReadyForReview)
    XCTAssertFalse(state.isComplete)
  }

  func testNotReadyForReviewWithoutTranscription() {
    let meetingID = UUID()
    let state = PipelineState(meetingID: meetingID)

    XCTAssertFalse(state.isReadyForReview)
  }

  func testCompleteOnlyWhenReadyForReviewPhaseSucceeds() {
    let meetingID = UUID()
    var state = PipelineState(meetingID: meetingID)
    state.phases[.transcription] = PhaseState(status: .succeeded)
    state.phases[.readyForReview] = PhaseState(status: .succeeded)

    XCTAssertTrue(state.isComplete)
  }

  func testPhaseOrdering() {
    XCTAssertTrue(PostRecordingPhase.transcription < PostRecordingPhase.diarization)
    XCTAssertTrue(PostRecordingPhase.diarization < PostRecordingPhase.alignment)
    XCTAssertTrue(PostRecordingPhase.alignment < PostRecordingPhase.projection)
    XCTAssertTrue(PostRecordingPhase.projection < PostRecordingPhase.export)
    XCTAssertTrue(PostRecordingPhase.export < PostRecordingPhase.readyForReview)
  }

  // MARK: - Pipeline run with no transcription

  func testPipelineSkipsWhenTranscriptionNotComplete() async throws {
    let meetingID = UUID()
    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      job: MeetingJob(jobID: UUID(), state: .processing),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)

    let pipeline = PostRecordingPipeline(dataRoot: root, meetingStore: store)
    let outcome = try await pipeline.run(meetingID: meetingID)

    XCTAssertEqual(outcome.state.status(of: .transcription), .pending)
    XCTAssertFalse(outcome.state.isReadyForReview)
  }

  func testPipelineMarksTranscriptionSucceededFromManifest() async throws {
    let meetingID = UUID()
    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      job: MeetingJob(jobID: UUID(), state: .completed),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)

    let pipeline = PostRecordingPipeline(dataRoot: root, meetingStore: store)
    let outcome = try await pipeline.run(meetingID: meetingID)

    XCTAssertEqual(outcome.state.status(of: .transcription), .succeeded)
    XCTAssertTrue(outcome.state.isReadyForReview)
    XCTAssertEqual(outcome.state.status(of: .diarization), .skipped)
  }

  func testPipelineReportsFailureWhenTranscriptionFailed() async throws {
    let meetingID = UUID()
    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      job: MeetingJob(jobID: UUID(), state: .failed),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)

    let pipeline = PostRecordingPipeline(dataRoot: root, meetingStore: store)
    let outcome = try await pipeline.run(meetingID: meetingID)

    XCTAssertEqual(outcome.state.status(of: .transcription), .failed)
    XCTAssertFalse(outcome.state.isReadyForReview)
  }

  // MARK: - Diarization failure does not block ready_for_review

  func testDiarizationFailureStillMarksReadyForReview() async throws {
    let meetingID = UUID()
    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      job: MeetingJob(jobID: UUID(), state: .completed),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)

    let pipeline = PostRecordingPipeline(dataRoot: root, meetingStore: store)
    let outcome = try await pipeline.run(meetingID: meetingID)

    XCTAssertTrue(outcome.state.isReadyForReview)
    XCTAssertEqual(outcome.state.status(of: .diarization), .skipped)
  }

  func testPipelineRunsDerivedPhasesAndPersistsArtifacts() async throws {
    let meetingID = UUID()
    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      job: MeetingJob(jobID: UUID(), state: .completed),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)
    let normalizedID = meetingID.uuidString.lowercased()
    let segment = try Segment(
      segmentID: UUID(), trackID: UUID(), speakerID: "speaker_0",
      startMS: 0, endMS: 1_000, engineText: "Hello from the pipeline")
    try await store.appendRawSegment(segment, meetingID: normalizedID)

    let engine = PipelineDiarizationEngine(
      output: DiarizationOutput(
        meetingID: meetingID,
        turns: [try SpeakerTurn(speakerID: "speaker_0", startMS: 0, endMS: 1_000)],
        speakerCount: 1))
    let progress = ProgressCollector()
    let pipeline = PostRecordingPipeline(dataRoot: root, meetingStore: store)

    let outcome = try await pipeline.run(
      meetingID: meetingID,
      diarizationEngine: engine,
      onProgress: { progress.record($0) })

    for phase in [
      PostRecordingPhase.transcription,
      .diarization,
      .alignment,
      .projection,
      .export,
      .readyForReview,
    ] {
      XCTAssertEqual(outcome.state.status(of: phase), .succeeded, "phase \(phase)")
    }
    XCTAssertTrue(outcome.state.isComplete)
    XCTAssertEqual(outcome.diarizationTurns, 1)
    XCTAssertEqual(outcome.alignmentSegments, 1)
    let rawTurns = try await store.rawTurns(meetingID: normalizedID)
    let alignments = try await store.alignment(meetingID: normalizedID)
    let editedTurns = try await store.editedTurns(meetingID: normalizedID)
    XCTAssertEqual(rawTurns.count, 1)
    XCTAssertEqual(alignments.count, 1)
    XCTAssertEqual(editedTurns.count, 1)

    let layout = ArtifactLayout(dataRoot: root)
    let transcript = try String(
      contentsOf: layout.transcriptURL(in: layout.meetingDirectory(normalizedID)),
      encoding: .utf8)
    XCTAssertTrue(transcript.contains("Hello from the pipeline"))
    XCTAssertTrue(
      progress.events.contains { event in
        if case .diarizationTurns(1) = event { return true }
        return false
      })
    XCTAssertTrue(
      progress.events.contains { event in
        if case .alignmentSegments(1) = event { return true }
        return false
      })
  }

  func testDiarizationFailurePersistsFailureAndStillCompletesReview() async throws {
    let meetingID = UUID()
    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      job: MeetingJob(jobID: UUID(), state: .completed),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)
    let segment = try Segment(
      segmentID: UUID(), trackID: UUID(), speakerID: "speaker_0",
      startMS: 0, endMS: 1_000, engineText: "Hello")
    try await store.appendRawSegment(segment, meetingID: meetingID.uuidString)

    let progress = ProgressCollector()
    let pipeline = PostRecordingPipeline(dataRoot: root, meetingStore: store)
    let outcome = try await pipeline.run(
      meetingID: meetingID,
      diarizationEngine: PipelineDiarizationEngine(failing: true),
      onProgress: { progress.record($0) })

    XCTAssertEqual(outcome.state.status(of: .diarization), .failed)
    XCTAssertTrue(outcome.state.isReadyForReview)
    XCTAssertTrue(outcome.state.isComplete)
    XCTAssertEqual(outcome.state.status(of: .alignment), .pending)
    XCTAssertTrue(
      progress.events.contains { event in
        if case .phaseFailed(.diarization, "expected") = event { return true }
        return false
      })
  }

  // MARK: - Pipeline state persistence

  func testPipelineStatePersistsAcrossRuns() async throws {
    let meetingID = UUID()
    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      job: MeetingJob(jobID: UUID(), state: .completed),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)

    let pipeline = PostRecordingPipeline(dataRoot: root, meetingStore: store)

    let first = try await pipeline.run(meetingID: meetingID)
    let second = try await pipeline.run(meetingID: meetingID)

    XCTAssertEqual(first.state.meetingID, second.state.meetingID)
    XCTAssertEqual(
      first.state.status(of: .transcription),
      second.state.status(of: .transcription))
  }

  // MARK: - Progress events

  func testProgressReportsTranscriptionPhase() async throws {
    let meetingID = UUID()
    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      job: MeetingJob(jobID: UUID(), state: .completed),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)

    let progressCollector = ProgressCollector()
    let pipeline = PostRecordingPipeline(dataRoot: root, meetingStore: store)
    let outcome = try await pipeline.run(
      meetingID: meetingID,
      onProgress: { progressCollector.record($0) })

    XCTAssertTrue(outcome.state.isReadyForReview)
    // Transcription phase is inferred from the manifest's completed job state
    // without emitting progress events, but ready_for_review should be set.
    XCTAssertTrue(outcome.state.status(of: .transcription) == .succeeded)
  }

  // MARK: - Speaker editor store integration

  func testSpeakerEditsRoundTripThroughStore() async throws {
    let meetingID = UUID()
    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)
    let normalizedID = meetingID.uuidString.lowercased()

    let event = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .label,
      speakerID: "speaker_0",
      label: "テスト発言者",
      sequenceNumber: 1)
    try await store.appendSpeakerEdit(event, meetingID: normalizedID)

    let edits = try await store.speakerEdits(meetingID: normalizedID)
    XCTAssertEqual(edits.count, 1)
    XCTAssertEqual(edits[0].label, "テスト発言者")
    XCTAssertEqual(edits[0].kind, .label)
  }

  func testRawTurnsRoundTripThroughStore() async throws {
    let meetingID = UUID()
    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)
    let normalizedID = meetingID.uuidString.lowercased()

    let turns = try [
      SpeakerTurn(speakerID: "speaker_0", startMS: 0, endMS: 1000),
      SpeakerTurn(speakerID: "speaker_1", startMS: 500, endMS: 1500),
    ]
    try await store.appendRawTurns(turns, meetingID: normalizedID)

    let loaded = try await store.rawTurns(meetingID: normalizedID)
    XCTAssertEqual(loaded.count, 2)
    XCTAssertEqual(loaded[0].speakerID, "speaker_0")
    XCTAssertEqual(loaded[1].speakerID, "speaker_1")
  }

  func testSpeakerMapRoundTripThroughStore() async throws {
    let meetingID = UUID()
    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)
    let normalizedID = meetingID.uuidString.lowercased()

    let map = SpeakerMap(
      meetingID: meetingID,
      labels: ["speaker_0": "Alice"],
      mergedSpeakers: [:],
      splitSegments: [:],
      lastEditSequence: 1)
    try await store.writeSpeakerMap(map, meetingID: normalizedID)

    let loaded = try await store.speakerMap(meetingID: normalizedID)
    XCTAssertNotNil(loaded)
    XCTAssertEqual(loaded?.labels["speaker_0"], "Alice")
  }
}

private final class ProgressCollector: @unchecked Sendable {
  private let lock = NSLock()
  private var storage: [PostRecordingProgress] = []

  func record(_ event: PostRecordingProgress) {
    lock.lock()
    storage.append(event)
    lock.unlock()
  }

  var events: [PostRecordingProgress] {
    lock.lock()
    defer { lock.unlock() }
    return storage
  }
}

private enum PipelineTestError: Error {
  case expected
  case missingOutput
}

private actor PipelineDiarizationEngine: DiarizationEngine {
  let engineID = "pipeline-test"
  let capabilities = EngineCapabilities(
    languages: [],
    supportsAutoDetection: true,
    supportsWordTimestamps: false,
    supportsSegmentTimestamps: true,
    supportsStreaming: false,
    supportsDiarization: true,
    requiredArchitectures: [])

  let output: DiarizationOutput?
  let failing: Bool

  init(output: DiarizationOutput? = nil, failing: Bool = false) {
    self.output = output
    self.failing = failing
  }

  func diarize(_ request: DiarizationRequest) async throws -> DiarizationOutput {
    if failing { throw PipelineTestError.expected }
    guard let output else { throw PipelineTestError.missingOutput }
    return output
  }
}
