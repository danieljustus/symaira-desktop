import Foundation

/// Everything ``TranscriptionPipeline/run(_:engine:onProgress:)`` needs to
/// start a brand-new file-to-transcript run. Model/engine resolution and the
/// "is this model installed" check happen in the CLI layer, which is the
/// only layer that knows about ``ModelStore`` -- by the time this reaches the
/// pipeline, `engineID`/`modelID`/`modelVersion` are just provenance to
/// record, and `engine` is already a ready-to-use instance.
public struct TranscriptionRequestOptions: Sendable {
  public let sourceURL: URL
  public let title: String?
  /// `nil` means auto-detect.
  public let language: String?
  public let modelID: String
  public let modelVersion: String
  public let engineID: String

  public init(
    sourceURL: URL,
    title: String?,
    language: String?,
    modelID: String,
    modelVersion: String,
    engineID: String
  ) {
    self.sourceURL = sourceURL
    self.title = title
    self.language = language
    self.modelID = modelID
    self.modelVersion = modelVersion
    self.engineID = engineID
  }
}

/// Streamed progress. Consumers (the CLI) render this to stderr; it never
/// reaches stdout, which in `--json` mode carries only the single final
/// ``TranscriptionOutcome`` envelope.
public enum PipelineProgress: Sendable {
  case meetingCreated(UUID)
  case phase(TranscriptionPhase)
  case progress(Double)
  case segmentFinalized(Segment)
  case warning(TranscriptionWarning)
}

/// The result of one pipeline run: either a fresh ``TranscriptionPipeline/run(_:engine:onProgress:)``
/// or a ``TranscriptionPipeline/retry(meetingID:engine:modelID:modelVersion:onProgress:)``.
public struct TranscriptionOutcome: Equatable, Sendable {
  public let meetingID: UUID
  public let jobID: UUID
  public let status: JobStatus
  public let attempt: Int
  public let segmentCount: Int
  public let language: String?
  public let engineID: String
  public let modelID: String
  public let sourceHash: String
}

/// Drives one meeting from a local media file (or an existing meeting's
/// already-imported asset, for a retry) through import, the transcription
/// engine, and job-state bookkeeping to a durable, portable meeting artifact.
///
/// Built directly on ``JobCoordinator`` from #9: every status transition
/// here is one of `JobCoordinator`'s existing lifecycle operations, never a
/// bespoke shortcut. Segment persistence is incremental -- each finalized
/// segment is written to `segments.raw.jsonl` as soon as it is deduplicated
/// by ``SegmentAccumulator``, not batched until the end.
///
/// One pipeline instance drives one run at a time. ``requestCancellation()``
/// is safe to call concurrently with an in-flight ``run(_:engine:onProgress:)``
/// or ``retry(meetingID:engine:modelID:modelVersion:onProgress:)`` call on the
/// same instance -- that is precisely the seam the CLI's SIGINT handler and
/// tests use to request cooperative cancellation.
public actor TranscriptionPipeline {
  public let dataRoot: URL

  private let meetingStore: MeetingStore
  private let coordinator: JobCoordinator
  private let audioProbe: AudioAssetProbe
  private let audioImporter: AudioAssetImporter
  private let layout: ArtifactLayout

  private var accumulator = SegmentAccumulator()
  private var persistedSegmentCount = 0
  private var completionResult: TranscriptionCompletion?
  private var cancelRequested = false
  private var runningTask: Task<Void, Error>?
  private var activeMeetingID: UUID?
  private var activeHandle: LockHandle?

  public init(
    dataRoot: URL,
    meetingStore: MeetingStore? = nil,
    coordinator: JobCoordinator? = nil,
    audioProbe: AudioAssetProbe = AudioAssetProbe(),
    audioImporter: AudioAssetImporter = AudioAssetImporter()
  ) {
    self.dataRoot = dataRoot
    self.meetingStore = meetingStore ?? MeetingStore(dataRoot: dataRoot)
    self.coordinator = coordinator ?? JobCoordinator(dataRoot: dataRoot)
    self.audioProbe = audioProbe
    self.audioImporter = audioImporter
    layout = ArtifactLayout(dataRoot: dataRoot)
  }

  /// Starts a brand-new meeting. The meeting ID is minted and reported via
  /// `onProgress(.meetingCreated)` before anything is imported, so a caller
  /// can print and durably recover it even if the run never completes.
  @discardableResult
  public func run(
    _ options: TranscriptionRequestOptions,
    engine: any TranscriptionEngine,
    onProgress: @escaping @Sendable (PipelineProgress) -> Void = { _ in }
  ) async throws -> TranscriptionOutcome {
    let meetingID = UUID()
    onProgress(.meetingCreated(meetingID))

    let asset = try await audioProbe.probe(options.sourceURL)

    var additionalFields: [String: JSONValue] = [:]
    if let title = options.title { additionalFields["title"] = .string(title) }
    let createdAt = Date()
    let manifest = MeetingManifest(
      meetingID: meetingID,
      source: .imported,
      createdAt: createdAt,
      updatedAt: createdAt,
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep),
      additionalFields: additionalFields)
    try await meetingStore.create(manifest)

    let job = try await coordinator.enqueue(meetingID: meetingID)
    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    activeMeetingID = meetingID
    activeHandle = handle
    cancelRequested = false

    do {
      _ = try await coordinator.advance(meetingID: meetingID, to: .preparing, using: handle)
      onProgress(.phase(.preparing))

      let meetingDirectory = layout.meetingDirectory(meetingID.uuidString.lowercased())
      let imported = try await audioImporter.importAsset(asset, into: meetingDirectory)
      let trackID = UUID()
      let engineProvenance = EngineProvenance(
        engineID: options.engineID, modelID: options.modelID, modelVersion: options.modelVersion)
      let preparedManifest = MeetingManifest(
        meetingID: manifest.meetingID,
        source: manifest.source,
        createdAt: manifest.createdAt,
        updatedAt: Date(),
        originalAsset: imported.relativePath,
        originalAssetMetadata: imported.metadata,
        audioTracks: [
          AudioTrack(trackID: trackID, kind: .original, relativePath: imported.relativePath)
        ],
        language: manifest.language,
        job: MeetingJob(jobID: job.jobID, state: .processing, engine: engineProvenance),
        consent: manifest.consent,
        retention: manifest.retention,
        additionalFields: manifest.additionalFields)
      try await meetingStore.update(preparedManifest)

      let outcome = try await transcribeAndFinalize(
        meetingID: meetingID,
        jobID: job.jobID,
        manifest: preparedManifest,
        trackID: trackID,
        audioURL: meetingDirectory.appending(
          path: imported.relativePath,
          directoryHint: .notDirectory),
        durationMS: asset.metadata.durationMS,
        sourceHash: asset.metadata.sha256,
        engine: engine,
        requestedEngineID: options.engineID,
        requestedModelID: options.modelID,
        requestedModelVersion: options.modelVersion,
        requestedLanguage: options.language,
        handle: handle,
        onProgress: onProgress)
      activeMeetingID = nil
      activeHandle = nil
      return outcome
    } catch {
      activeMeetingID = nil
      activeHandle = nil
      try? await coordinator.lock.release(handle)
      throw error
    }
  }

  /// Restarts an existing meeting's transcription from the beginning,
  /// reusing the already-imported original asset (no re-probe, no re-copy).
  /// Matches ``JobCoordinator/retry(meetingID:using:)``: a full restart, not
  /// a checkpoint resume. Already-finalized segments from the prior attempt
  /// are read back and seeded into ``SegmentAccumulator`` so they are never
  /// persisted twice.
  @discardableResult
  public func retry(
    meetingID: UUID,
    engine: any TranscriptionEngine,
    modelID: String,
    modelVersion: String,
    onProgress: @escaping @Sendable (PipelineProgress) -> Void = { _ in }
  ) async throws -> TranscriptionOutcome {
    let normalizedID = meetingID.uuidString.lowercased()
    let manifest = try await meetingStore.load(meetingID: normalizedID)
    guard let originalAsset = manifest.originalAsset,
      let metadata = manifest.originalAssetMetadata
    else {
      throw PipelineError.missingOriginalAsset
    }
    let trackID = manifest.audioTracks.first { $0.relativePath == originalAsset }?.trackID ?? UUID()

    let handle = try await coordinator.lock.acquire(meetingID: meetingID)
    activeMeetingID = meetingID
    activeHandle = handle
    cancelRequested = false

    do {
      let retriedJob = try await coordinator.retry(meetingID: meetingID, using: handle)
      _ = try await coordinator.advance(meetingID: meetingID, to: .preparing, using: handle)
      onProgress(.phase(.preparing))

      let existingSegments = try await meetingStore.rawSegments(meetingID: normalizedID)
      let engineID = await engine.engineIdentifier
      let engineProvenance = EngineProvenance(
        engineID: engineID, modelID: modelID, modelVersion: modelVersion)
      var refreshedManifest = manifest
      refreshedManifest.job = MeetingJob(
        jobID: retriedJob.jobID,
        state: .processing,
        engine: engineProvenance)
      try await meetingStore.update(refreshedManifest)

      let meetingDirectory = layout.meetingDirectory(normalizedID)
      let outcome = try await transcribeAndFinalize(
        meetingID: meetingID,
        jobID: retriedJob.jobID,
        manifest: refreshedManifest,
        trackID: trackID,
        audioURL: meetingDirectory.appending(path: originalAsset, directoryHint: .notDirectory),
        durationMS: metadata.durationMS,
        sourceHash: metadata.sha256,
        engine: engine,
        requestedEngineID: await engine.engineIdentifier,
        requestedModelID: modelID,
        requestedModelVersion: modelVersion,
        requestedLanguage: manifest.language,
        handle: handle,
        existingSegments: existingSegments,
        onProgress: onProgress)
      activeMeetingID = nil
      activeHandle = nil
      return outcome
    } catch {
      activeMeetingID = nil
      activeHandle = nil
      try? await coordinator.lock.release(handle)
      throw error
    }
  }

  /// Requests cooperative cancellation of whichever run is currently active
  /// on this instance. Safe to call from a SIGINT handler or, in tests,
  /// directly instead of delivering a literal signal. Returns only once the
  /// in-flight engine task has been cancelled; the caller must still await
  /// the original `run`/`retry` call to observe the durable terminal state.
  public func requestCancellation() async {
    cancelRequested = true
    if let meetingID = activeMeetingID, let handle = activeHandle {
      _ = try? await coordinator.requestCancellation(meetingID: meetingID, using: handle)
    }
    runningTask?.cancel()
  }

  // MARK: Shared engine-driving core

  private func transcribeAndFinalize(
    meetingID: UUID,
    jobID: UUID,
    manifest: MeetingManifest,
    trackID: UUID,
    audioURL: URL,
    durationMS: Int,
    sourceHash: String,
    engine: any TranscriptionEngine,
    requestedEngineID: String,
    requestedModelID: String,
    requestedModelVersion: String,
    requestedLanguage: String?,
    handle: LockHandle,
    existingSegments: [Segment] = [],
    onProgress: @escaping @Sendable (PipelineProgress) -> Void
  ) async throws -> TranscriptionOutcome {
    _ = try await coordinator.advance(meetingID: meetingID, to: .transcribing, using: handle)
    onProgress(.phase(.transcribing))

    accumulator = SegmentAccumulator(existing: existingSegments)
    persistedSegmentCount = existingSegments.count
    completionResult = nil

    let request = TranscriptionRequest(
      audio: AudioSampleReader(url: audioURL).chunks(),
      trackID: trackID,
      modelID: requestedModelID,
      language: requestedLanguage,
      sourceDurationMS: durationMS)

    let task = Task {
      try await self.consumeEvents(engine: engine, request: request, onProgress: onProgress)
    }
    runningTask = task
    do {
      try await task.value
    } catch is CancellationError {
      // Outcome is decided below purely from `cancelRequested`, which is
      // always set before this task is cancelled -- see requestCancellation().
    } catch {
      runningTask = nil
      if !cancelRequested {
        let message = (error as? LocalizedError)?.errorDescription ?? String(describing: error)
        _ = try? await coordinator.fail(
          meetingID: meetingID, classification: .retryable, code: "engine_failed", message: message,
          using: handle)
        try? await coordinator.lock.release(handle)
        throw PipelineError.engineFailed(message)
      }
    }
    runningTask = nil

    if cancelRequested {
      return try await finalizeCancellation(
        meetingID: meetingID, jobID: jobID, manifest: manifest, sourceHash: sourceHash,
        requestedEngineID: requestedEngineID, requestedModelID: requestedModelID, handle: handle)
    }

    guard let completionResult else {
      _ = try? await coordinator.fail(
        meetingID: meetingID, classification: .retryable, code: "engine_incomplete",
        message: PipelineError.engineProducedNoCompletion.localizedDescription, using: handle)
      try? await coordinator.lock.release(handle)
      throw PipelineError.engineProducedNoCompletion
    }

    _ = try await coordinator.advance(meetingID: meetingID, to: .exporting, using: handle)
    onProgress(.phase(.exporting))

    // A cancellation request that lands exactly as processing finished must
    // still win over marking the job succeeded.
    if cancelRequested {
      return try await finalizeCancellation(
        meetingID: meetingID, jobID: jobID, manifest: manifest, sourceHash: sourceHash,
        requestedEngineID: requestedEngineID, requestedModelID: requestedModelID, handle: handle)
    }

    let finalLanguage = completionResult.language ?? requestedLanguage
    let engineProvenance = EngineProvenance(
      engineID: requestedEngineID, modelID: requestedModelID, modelVersion: requestedModelVersion)
    let finalManifest = MeetingManifest(
      meetingID: manifest.meetingID,
      source: manifest.source,
      createdAt: manifest.createdAt,
      updatedAt: Date(),
      originalAsset: manifest.originalAsset,
      originalAssetMetadata: manifest.originalAssetMetadata,
      audioTracks: manifest.audioTracks,
      language: finalLanguage,
      job: MeetingJob(jobID: jobID, state: .completed, engine: engineProvenance),
      consent: manifest.consent,
      retention: manifest.retention,
      additionalFields: manifest.additionalFields)

    let succeeded = try await coordinator.succeed(meetingID: meetingID, using: handle) {
      try await self.meetingStore.update(finalManifest)
    }
    try await coordinator.lock.release(handle)

    return TranscriptionOutcome(
      meetingID: meetingID,
      jobID: jobID,
      status: succeeded.status,
      attempt: succeeded.attempt,
      segmentCount: persistedSegmentCount,
      language: finalLanguage,
      engineID: requestedEngineID,
      modelID: requestedModelID,
      sourceHash: sourceHash)
  }

  private func finalizeCancellation(
    meetingID: UUID,
    jobID: UUID,
    manifest: MeetingManifest,
    sourceHash: String,
    requestedEngineID: String,
    requestedModelID: String,
    handle: LockHandle
  ) async throws -> TranscriptionOutcome {
    _ = try? await coordinator.confirmCancelled(meetingID: meetingID, using: handle)
    try? await coordinator.lock.release(handle)
    let job = try await coordinator.load(meetingID: meetingID)
    return TranscriptionOutcome(
      meetingID: meetingID,
      jobID: jobID,
      status: job.status,
      attempt: job.attempt,
      segmentCount: persistedSegmentCount,
      language: manifest.language,
      engineID: requestedEngineID,
      modelID: requestedModelID,
      sourceHash: sourceHash)
  }

  private func consumeEvents(
    engine: any TranscriptionEngine,
    request: TranscriptionRequest,
    onProgress: @escaping @Sendable (PipelineProgress) -> Void
  ) async throws {
    for try await event in await engine.transcribe(request) {
      try await handle(event: event, onProgress: onProgress)
    }
  }

  private func handle(
    event: TranscriptionEvent,
    onProgress: @escaping @Sendable (PipelineProgress) -> Void
  ) async throws {
    switch event.type {
    case .phase:
      if let phase = event.phase { onProgress(.phase(phase)) }
    case .progress:
      if let value = event.progress { onProgress(.progress(value)) }
    case .finalizedSegment:
      guard let draft = event.segment, let meetingID = activeMeetingID else { return }
      guard let segment = try accumulator.finalize(draft) else { return }
      try await meetingStore.appendRawSegment(segment, meetingID: meetingID.uuidString.lowercased())
      persistedSegmentCount += 1
      onProgress(.segmentFinalized(segment))
    case .checkpoint:
      guard let checkpoint = event.checkpoint,
        let meetingID = activeMeetingID,
        let handle = activeHandle
      else { return }
      _ = try? await coordinator.recordCheckpoint(
        meetingID: meetingID,
        checkpoint: checkpoint,
        using: handle)
    case .warning:
      if let warning = event.warning { onProgress(.warning(warning)) }
    case .partialSegment:
      break
    case .completed:
      completionResult = event.completion
    }
  }
}

extension TranscriptionEngine {
  /// `engineID` is declared on the protocol without `nonisolated`, so
  /// accessing it from outside the engine's actor isolation is an async
  /// hop. This gives the pipeline a single, clearly-named crossing point.
  fileprivate var engineIdentifier: String {
    get async { engineID }
  }
}
