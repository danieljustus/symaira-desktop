import Foundation

// MARK: - Seams the platform host implements

/// Captures microphone audio between `start()` and `finish()`/`cancel()`.
///
/// Implementations must record only inside that window and keep the samples in memory.
@MainActor
public protocol DictationRecorder: AnyObject {
  func start() throws
  /// Stops recording and returns 16 kHz mono float samples.
  func finish() -> [Float]
  /// Stops recording and discards everything.
  func cancel()
}

/// Speech-to-text for one finished recording.
public protocol DictationTranscriber: Sendable {
  func transcribe(samples: [Float]) async throws -> String
}

/// Something the user should be told about that is not a card.
public enum DictationNotice: Equatable, Sendable {
  case microphoneUnavailable(String)
  case transcriptionFailed(String)
  case nothingHeard
}

/// The visible side of dictation: indicator pill, text card and short notices.
@MainActor
public protocol DictationPresenter: AnyObject {
  func showRecording(_ mode: DictationRecordingMode)
  func showProcessing()
  func hideIndicator()
  /// Keeps `text` visible with a copy button because it could not be inserted.
  func showCard(text: String, reason: CardReason)
  func showNotice(_ notice: DictationNotice)
}

/// Reads the live state of the system around the focused application.
@MainActor
public protocol DictationEnvironment: AnyObject {
  func currentTarget() -> InsertionTarget
  func currentConditions() -> InsertionConditions
  func currentContext() -> DictationContext
}

// MARK: - Controller

/// Runs one dictation after another: gesture events in, text in the focused app (or a card) out.
///
/// All system access goes through protocols, so the whole flow is testable without a
/// microphone, an event tap or a pasteboard.
@MainActor
public final class DictationController {
  private var machine: DictationGestureMachine
  private let recorder: any DictationRecorder
  private let transcriber: any DictationTranscriber
  private let pipelineProvider: @MainActor () -> DictationPipeline
  private let environment: any DictationEnvironment
  private let inserter: DictationInserter
  private let presenter: any DictationPresenter
  private let minimumSamples: Int
  private let indicatorDelay: TimeInterval

  private var indicatorTask: Task<Void, Never>?
  private var processingTask: Task<Void, Never>?
  private var generation: UInt64 = 0

  /// Recordings shorter than this (at 16 kHz) are treated as accidental and ignored.
  public static let defaultMinimumSamples = 4_800

  public init(
    machine: DictationGestureMachine = DictationGestureMachine(),
    recorder: any DictationRecorder,
    transcriber: any DictationTranscriber,
    pipelineProvider: @escaping @MainActor () -> DictationPipeline = { DictationPipeline() },
    environment: any DictationEnvironment,
    inserter: DictationInserter,
    presenter: any DictationPresenter,
    minimumSamples: Int = DictationController.defaultMinimumSamples,
    indicatorDelay: TimeInterval = DictationGestureMachine.defaultShortPressLimit
  ) {
    self.machine = machine
    self.recorder = recorder
    self.transcriber = transcriber
    self.pipelineProvider = pipelineProvider
    self.environment = environment
    self.inserter = inserter
    self.presenter = presenter
    self.minimumSamples = minimumSamples
    self.indicatorDelay = indicatorDelay
  }

  /// A recording or processing session is running (used to decide whether Esc is ours).
  public var isActive: Bool {
    switch machine.state {
    case .recording, .processing: return true
    case .idle, .cancelled: return false
    }
  }

  public var state: DictationGestureState { machine.state }

  public func handle(_ event: DictationGestureEvent) {
    if event == .escape, machine.state == .processing {
      cancelProcessing()
      return
    }
    switch machine.handle(event) {
    case .none:
      break
    case .startRecording:
      startRecording()
    case .stopRecordingAndProcess:
      stopAndProcess()
    case .cancelRecording:
      cancelRecording()
    }
  }

  /// Aborts everything, e.g. when dictation is switched off or the app quits.
  public func shutdown() {
    indicatorTask?.cancel()
    processingTask?.cancel()
    processingTask = nil
    recorder.cancel()
    inserter.cancelSession()
    inserter.restoreAbandonedPaste()
    presenter.hideIndicator()
    machine = DictationGestureMachine(
      shortPressLimit: machine.shortPressLimit, doubleTapWindow: machine.doubleTapWindow)
  }

  /// Waits for a running transcription/delivery; for tests and orderly shutdown.
  public func waitUntilIdle() async {
    await processingTask?.value
  }

  // MARK: - Effects

  private func startRecording() {
    generation = inserter.beginSession(documentID: environment.currentTarget().documentID)
    do {
      try recorder.start()
    } catch {
      _ = machine.handle(.escape)
      inserter.cancelSession()
      presenter.showNotice(.microphoneUnavailable(error.localizedDescription))
      return
    }
    scheduleIndicator()
  }

  private func scheduleIndicator() {
    indicatorTask?.cancel()
    guard indicatorDelay > 0 else {
      showRecordingIndicator()
      return
    }
    let delay = indicatorDelay
    indicatorTask = Task { [weak self] in
      try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
      guard !Task.isCancelled else { return }
      self?.showRecordingIndicator()
    }
  }

  private func showRecordingIndicator() {
    if case .recording(let mode) = machine.state {
      presenter.showRecording(mode)
    }
  }

  private func cancelRecording() {
    indicatorTask?.cancel()
    recorder.cancel()
    inserter.cancelSession()
    inserter.restoreAbandonedPaste()
    presenter.hideIndicator()
  }

  private func cancelProcessing() {
    processingTask?.cancel()
    processingTask = nil
    inserter.cancelSession()
    inserter.restoreAbandonedPaste()
    presenter.hideIndicator()
    _ = machine.handle(.processingFinished)
  }

  private func stopAndProcess() {
    indicatorTask?.cancel()
    let samples = recorder.finish()
    let session = generation
    guard samples.count >= minimumSamples else {
      inserter.cancelSession()
      inserter.restoreAbandonedPaste()
      presenter.hideIndicator()
      _ = machine.handle(.processingFinished)
      return
    }
    presenter.showProcessing()
    let context = environment.currentContext()
    let pipeline = pipelineProvider()
    processingTask = Task { [weak self] in
      guard let self else { return }
      await self.process(samples, session: session, context: context, pipeline: pipeline)
    }
  }

  private func process(
    _ samples: [Float],
    session: UInt64,
    context: DictationContext,
    pipeline: DictationPipeline
  ) async {
    let raw: String
    do {
      raw = try await transcriber.transcribe(samples: samples)
    } catch {
      guard !Task.isCancelled else { return }
      finishProcessing(notice: .transcriptionFailed(error.localizedDescription))
      return
    }
    guard !Task.isCancelled else { return }

    let result = await pipeline.process(raw, context: context)
    guard !Task.isCancelled else { return }

    let text = result.finalText.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !text.isEmpty else {
      finishProcessing(notice: .nothingHeard)
      return
    }

    let outcome = await inserter.deliver(
      text,
      generation: session,
      target: environment.currentTarget(),
      conditions: environment.currentConditions())
    guard !Task.isCancelled else { return }
    if case .card(let reason) = outcome {
      presenter.showCard(text: text, reason: reason)
    }
    finishProcessing(notice: nil)
  }

  private func finishProcessing(notice: DictationNotice?) {
    inserter.restoreAbandonedPaste()
    presenter.hideIndicator()
    if let notice { presenter.showNotice(notice) }
    processingTask = nil
    _ = machine.handle(.processingFinished)
  }
}
