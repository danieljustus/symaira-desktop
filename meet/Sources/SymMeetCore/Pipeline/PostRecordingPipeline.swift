import Foundation

/// The durable phases of the post-recording processing pipeline.
public enum PostRecordingPhase: String, Codable, CaseIterable, Sendable, Comparable {
  case transcription
  case diarization
  case alignment
  case projection
  case export
  case readyForReview = "ready_for_review"

  public static func < (lhs: Self, rhs: Self) -> Bool {
    let order: [PostRecordingPhase] = [
      .transcription, .diarization, .alignment, .projection, .export, .readyForReview,
    ]
    return order.firstIndex(of: lhs)! < order.firstIndex(of: rhs)!
  }
}

public enum PostRecordingPhaseStatus: String, Codable, Sendable {
  case pending
  case running
  case succeeded
  case failed
  case skipped
}

public struct PhaseState: Codable, Equatable, Sendable {
  public let status: PostRecordingPhaseStatus
  public let message: String?

  public init(status: PostRecordingPhaseStatus, message: String? = nil) {
    self.status = status
    self.message = message
  }
}

/// Durable pipeline state persisted as `pipeline_state.json`.
public struct PipelineState: Codable, Equatable, Sendable {
  public static let supportedSchemaVersion = 1

  public let schemaVersion: Int
  public let meetingID: UUID
  public var phases: [PostRecordingPhase: PhaseState]

  public init(meetingID: UUID, phases: [PostRecordingPhase: PhaseState] = [:]) {
    schemaVersion = Self.supportedSchemaVersion
    self.meetingID = meetingID
    self.phases = phases
  }

  private enum CodingKeys: String, CodingKey, CaseIterable {
    case schemaVersion = "schema_version"
    case meetingID = "meeting_id"
    case phases
  }

  public init(from decoder: Decoder) throws {
    let container = try decoder.container(keyedBy: CodingKeys.self)
    let schemaVersion = try container.decode(Int.self, forKey: .schemaVersion)
    guard schemaVersion == Self.supportedSchemaVersion else {
      throw ContractError.unsupportedSchemaVersion(schemaVersion)
    }
    self.schemaVersion = schemaVersion
    meetingID = try container.decode(UUID.self, forKey: .meetingID)
    phases = try container.decode([PostRecordingPhase: PhaseState].self, forKey: .phases)
  }

  public func encode(to encoder: Encoder) throws {
    var container = encoder.container(keyedBy: CodingKeys.self)
    try container.encode(Self.supportedSchemaVersion, forKey: .schemaVersion)
    try container.encode(meetingID, forKey: .meetingID)
    try container.encode(phases, forKey: .phases)
  }

  public func status(of phase: PostRecordingPhase) -> PostRecordingPhaseStatus {
    phases[phase]?.status ?? .pending
  }

  public var highestCompletedPhase: PostRecordingPhase? {
    PostRecordingPhase.allCases.last(where: { phases[$0]?.status == .succeeded })
  }

  public var isReadyForReview: Bool {
    status(of: .transcription) == .succeeded
  }

  public var isComplete: Bool {
    phases[.readyForReview]?.status == .succeeded
  }
}

public enum PostRecordingProgress: Sendable {
  case phaseStarted(PostRecordingPhase)
  case phaseSucceeded(PostRecordingPhase)
  case phaseFailed(PostRecordingPhase, String)
  case diarizationTurns(Int)
  case alignmentSegments(Int)
  case exportWritten(String)
}

public struct PostRecordingOutcome: Equatable, Sendable {
  public let meetingID: UUID
  public let state: PipelineState
  public let diarizationTurns: Int
  public let alignmentSegments: Int
  public let editedSegments: Int
}

/// Drives an existing meeting through diarization, alignment, edited
/// projection, and export as separate durable phases.
///
/// Phase recovery: when the pipeline is restarted for a meeting that has
/// partial progress, it resumes at the first incomplete derived phase.
/// Re-running one phase invalidates only downstream derived artifacts.
public actor PostRecordingPipeline {
  public let dataRoot: URL

  private let meetingStore: MeetingStore
  private let layout: ArtifactLayout
  private let editor: SpeakerEditor

  public init(
    dataRoot: URL,
    meetingStore: MeetingStore? = nil
  ) {
    self.dataRoot = dataRoot
    self.meetingStore = meetingStore ?? MeetingStore(dataRoot: dataRoot)
    self.layout = ArtifactLayout(dataRoot: dataRoot)
    self.editor = SpeakerEditor()
  }

  public func run(
    meetingID: UUID,
    diarizationEngine: (any DiarizationEngine)? = nil,
    numberOfSpeakers: Int? = nil,
    forceRediarize: Bool = false,
    onProgress: @escaping @Sendable (PostRecordingProgress) -> Void = { _ in }
  ) async throws -> PostRecordingOutcome {
    let normalizedID = meetingID.uuidString.lowercased()
    let manifest = try await meetingStore.load(meetingID: normalizedID)

    var state =
      try await meetingStore.pipelineState(meetingID: normalizedID)
      ?? PipelineState(meetingID: meetingID)

    // Phase 1: Transcription (already handled by TranscriptionPipeline).
    if state.status(of: .transcription) != .succeeded {
      if manifest.job?.state == .completed {
        state.phases[.transcription] = PhaseState(status: .succeeded)
        try await meetingStore.writePipelineState(state, meetingID: normalizedID)
      } else if manifest.job?.state == .failed {
        state.phases[.transcription] = PhaseState(
          status: .failed, message: "Transcription job failed.")
        try await meetingStore.writePipelineState(state, meetingID: normalizedID)
        return PostRecordingOutcome(
          meetingID: meetingID, state: state,
          diarizationTurns: 0, alignmentSegments: 0, editedSegments: 0)
      } else {
        state.phases[.transcription] = PhaseState(
          status: .pending, message: "Transcription not yet complete.")
        try await meetingStore.writePipelineState(state, meetingID: normalizedID)
        return PostRecordingOutcome(
          meetingID: meetingID, state: state,
          diarizationTurns: 0, alignmentSegments: 0, editedSegments: 0)
      }
    }

    // Phase 2: Diarization
    if forceRediarize || state.status(of: .diarization) != .succeeded {
      if let engine = diarizationEngine {
        onProgress(.phaseStarted(.diarization))
        state.phases[.diarization] = PhaseState(status: .running)
        try await meetingStore.writePipelineState(state, meetingID: normalizedID)

        do {
          let turnCount = try await runDiarization(
            meetingID: meetingID, manifest: manifest,
            engine: engine, numberOfSpeakers: numberOfSpeakers)
          state.phases[.diarization] = PhaseState(status: .succeeded)
          onProgress(.phaseSucceeded(.diarization))
          onProgress(.diarizationTurns(turnCount))

          if forceRediarize {
            state.phases[.alignment] = PhaseState(status: .pending)
            state.phases[.projection] = PhaseState(status: .pending)
            state.phases[.export] = PhaseState(status: .pending)
            state.phases[.readyForReview] = PhaseState(status: .pending)
          }
          try await meetingStore.writePipelineState(state, meetingID: normalizedID)
        } catch {
          let message = Self.localizedError(error)
          state.phases[.diarization] = PhaseState(status: .failed, message: message)
          try await meetingStore.writePipelineState(state, meetingID: normalizedID)
          onProgress(.phaseFailed(.diarization, message))
        }
      } else {
        state.phases[.diarization] = PhaseState(status: .skipped)
        try await meetingStore.writePipelineState(state, meetingID: normalizedID)
      }
    }

    // Phase 3: Alignment
    if state.status(of: .alignment) != .succeeded
      && state.status(of: .diarization) == .succeeded
    {
      onProgress(.phaseStarted(.alignment))
      state.phases[.alignment] = PhaseState(status: .running)
      try await meetingStore.writePipelineState(state, meetingID: normalizedID)

      do {
        let count = try await runAlignment(meetingID: normalizedID)
        state.phases[.alignment] = PhaseState(status: .succeeded)
        onProgress(.phaseSucceeded(.alignment))
        onProgress(.alignmentSegments(count))
        try await meetingStore.writePipelineState(state, meetingID: normalizedID)
      } catch {
        let message = Self.localizedError(error)
        state.phases[.alignment] = PhaseState(status: .failed, message: message)
        try await meetingStore.writePipelineState(state, meetingID: normalizedID)
        onProgress(.phaseFailed(.alignment, message))
      }
    }

    // Phase 4: Edited projection
    if state.status(of: .projection) != .succeeded
      && state.status(of: .alignment) == .succeeded
    {
      onProgress(.phaseStarted(.projection))
      state.phases[.projection] = PhaseState(status: .running)
      try await meetingStore.writePipelineState(state, meetingID: normalizedID)

      do {
        _ = try await runProjection(meetingID: normalizedID)
        state.phases[.projection] = PhaseState(status: .succeeded)
        onProgress(.phaseSucceeded(.projection))
        try await meetingStore.writePipelineState(state, meetingID: normalizedID)
      } catch {
        let message = Self.localizedError(error)
        state.phases[.projection] = PhaseState(status: .failed, message: message)
        try await meetingStore.writePipelineState(state, meetingID: normalizedID)
        onProgress(.phaseFailed(.projection, message))
      }
    }

    // Phase 5: Export
    if state.status(of: .export) != .succeeded
      && state.status(of: .projection) == .succeeded
    {
      onProgress(.phaseStarted(.export))
      state.phases[.export] = PhaseState(status: .running)
      try await meetingStore.writePipelineState(state, meetingID: normalizedID)

      do {
        try await runExport(meetingID: normalizedID, manifest: manifest)
        state.phases[.export] = PhaseState(status: .succeeded)
        onProgress(.phaseSucceeded(.export))
        try await meetingStore.writePipelineState(state, meetingID: normalizedID)
      } catch {
        let message = Self.localizedError(error)
        state.phases[.export] = PhaseState(status: .failed, message: message)
        try await meetingStore.writePipelineState(state, meetingID: normalizedID)
        onProgress(.phaseFailed(.export, message))
      }
    }

    // ready_for_review: set when transcription succeeded
    if state.status(of: .readyForReview) != .succeeded && manifest.job?.state == .completed {
      state.phases[.readyForReview] = PhaseState(status: .succeeded)
      try await meetingStore.writePipelineState(state, meetingID: normalizedID)
    }

    let turnCount = (try? await meetingStore.rawTurns(meetingID: normalizedID))?.count ?? 0
    let alignmentCount = (try? await meetingStore.alignment(meetingID: normalizedID))?.count ?? 0
    let editedCount = (try? await meetingStore.editedSegments(meetingID: normalizedID))?.count ?? 0

    return PostRecordingOutcome(
      meetingID: meetingID,
      state: state,
      diarizationTurns: turnCount,
      alignmentSegments: alignmentCount,
      editedSegments: editedCount)
  }

  // MARK: - Phase implementations

  private func runDiarization(
    meetingID: UUID,
    manifest: MeetingManifest,
    engine: any DiarizationEngine,
    numberOfSpeakers: Int?
  ) async throws -> Int {
    let normalizedID = meetingID.uuidString.lowercased()

    let segments = try await meetingStore.rawSegments(meetingID: normalizedID)
    guard !segments.isEmpty else {
      throw PipelineError.noSegmentsForDiarization
    }
    let maxEndMS = segments.map(\.endMS).max() ?? 0
    guard maxEndMS > 0 else {
      throw PipelineError.noSegmentsForDiarization
    }

    let sourceKind: DiarizationSourceKind =
      manifest.audioTracks.count > 1 ? .nativeDualTrack : .importedMixed
    let audioSamples: [Float] = []
    let microphoneSamples: [Float]? =
      sourceKind == .nativeDualTrack ? [] : nil

    let request = DiarizationRequest(
      sourceKind: sourceKind,
      meetingID: meetingID,
      audioSamples: audioSamples,
      microphoneSamples: microphoneSamples,
      numberOfSpeakers: numberOfSpeakers,
      durationMS: maxEndMS)

    let output = try await engine.diarize(request)

    try await meetingStore.appendRawTurns(
      output.turns, meetingID: normalizedID)

    return output.turns.count
  }

  private func runAlignment(meetingID: String) async throws -> Int {
    let segments = try await meetingStore.rawSegments(meetingID: meetingID)
    let turns = try await meetingStore.rawTurns(meetingID: meetingID)

    guard !segments.isEmpty else {
      throw PipelineError.noSegmentsForAlignment
    }

    let meetingIDUUID = UUID(uuidString: meetingID) ?? UUID()
    let alignments = try SpeakerAligner.align(
      segments: segments, turns: turns, meetingID: meetingIDUUID)

    try await meetingStore.writeAlignment(alignments, meetingID: meetingID)
    return alignments.count
  }

  private func runProjection(meetingID: String) async throws -> Int {
    let edits = try await meetingStore.speakerEdits(meetingID: meetingID)
    let meetingIDUUID = UUID(uuidString: meetingID) ?? UUID()

    let turns = try await meetingStore.rawTurns(meetingID: meetingID)
    let knownSpeakerIDs = Set(turns.map(\.speakerID))

    let segments = try await meetingStore.rawSegments(meetingID: meetingID)
    let knownSegmentIDs = Set(segments.map(\.segmentID))

    let map = try editor.replay(
      events: edits,
      knownSpeakerIDs: knownSpeakerIDs,
      knownSegmentIDs: knownSegmentIDs,
      meetingID: meetingIDUUID)
    try await meetingStore.writeSpeakerMap(map, meetingID: meetingID)

    let editedTurns = try editor.projectTurns(turns, using: map)
    try await meetingStore.writeEditedTurns(editedTurns, meetingID: meetingID)

    return editedTurns.count
  }

  private func runExport(meetingID: String, manifest: MeetingManifest) async throws {
    let segments = try await meetingStore.rawSegments(meetingID: meetingID)
    let editedSegs = (try? await meetingStore.editedSegments(meetingID: meetingID)) ?? []

    let segmentsToRender = editedSegs.isEmpty ? segments : editedSegs
    let content = try TranscriptRenderer.render(
      manifest: manifest,
      segments: segmentsToRender,
      segmentSource: editedSegs.isEmpty ? .raw : .edited,
      format: .markdown,
      options: TranscriptRenderer.Options(withTimestamps: false))

    let directory = layout.meetingDirectory(meetingID)
    try requireSafePath(directory)
    try AtomicFileWriter.write(
      Data(content.utf8), to: layout.transcriptURL(in: directory))
  }

  private static func localizedError(_ error: Error) -> String {
    (error as? LocalizedError)?.errorDescription ?? String(describing: error)
  }

  private func requireSafePath(_ candidate: URL) throws {
    let root = layout.dataRoot.standardizedFileURL
    let path = candidate.standardizedFileURL
    guard path.path == root.path || path.path.hasPrefix(root.path + "/") else {
      throw StoreError.unsafePath
    }
  }
}
