import Foundation
import SymMeetCore

enum FakeEngineOutcome: Equatable, Sendable {
  case complete
  case fail
  case cancel
}

enum FakeEngineError: Error, Equatable, Sendable {
  case requestedFailure
}

actor FakeTranscriptionEngine: TranscriptionEngine {
  nonisolated let engineID = "fake"
  nonisolated let capabilities = EngineCapabilities(
    languages: ["en", "de"],
    supportsAutoDetection: true,
    supportsWordTimestamps: false,
    supportsSegmentTimestamps: true,
    supportsStreaming: true,
    supportsDiarization: false,
    requiredArchitectures: ["arm64", "x86_64"])

  /// Fires once when `transcribe` reaches the paused point (after all chunks
  /// are finalized, right before completion), letting tests wait
  /// deterministically instead of sleeping on a timer.
  nonisolated let pausedSignal: AsyncStream<Void>
  private let pausedContinuation: AsyncStream<Void>.Continuation

  private var outcome: FakeEngineOutcome = .complete
  private var paused = false
  private var resumeWaiters: [CheckedContinuation<Void, Never>] = []

  init() {
    let (stream, continuation) = AsyncStream.makeStream(of: Void.self)
    pausedSignal = stream
    pausedContinuation = continuation
  }

  func setOutcome(_ outcome: FakeEngineOutcome) {
    self.outcome = outcome
  }

  func pauseBeforeCompletion() {
    paused = true
  }

  func resume() {
    paused = false
    let waiters = resumeWaiters
    resumeWaiters.removeAll()
    for waiter in waiters {
      waiter.resume()
    }
  }

  func transcribe(
    _ request: TranscriptionRequest
  ) -> AsyncThrowingStream<TranscriptionEvent, Error> {
    let outcome = outcome
    return AsyncThrowingStream(bufferingPolicy: .bufferingNewest(8)) { continuation in
      let task = Task {
        do {
          continuation.yield(
            TranscriptionEvent(type: .phase, phase: .preparing))
          continuation.yield(
            TranscriptionEvent(type: .phase, phase: .transcribing))

          var segments = 0
          for try await chunk in request.audio {
            if Task.isCancelled || outcome == .cancel {
              continuation.yield(
                TranscriptionEvent(type: .phase, phase: .cancelled))
              continuation.finish()
              return
            }
            if outcome == .fail {
              throw FakeEngineError.requestedFailure
            }

            segments += 1
            let draft = SegmentDraft(
              trackID: request.trackID,
              startMS: chunk.startMS,
              endMS: max(chunk.endMS, chunk.startMS + 1),
              text: "fake segment \(segments)")
            continuation.yield(
              TranscriptionEvent(type: .finalizedSegment, segment: draft))
            let progress =
              request.sourceDurationMS == 0
              ? 0
              : min(1, Double(chunk.endMS) / Double(request.sourceDurationMS))
            continuation.yield(TranscriptionEvent(type: .progress, progress: progress))
            continuation.yield(
              TranscriptionEvent(
                type: .checkpoint,
                checkpoint: TranscriptionCheckpoint(
                  completedSourceTimeMS: chunk.endMS,
                  engineID: engineID,
                  modelID: request.modelID)))
          }

          await waitIfPaused()
          if outcome == .cancel {
            continuation.yield(TranscriptionEvent(type: .phase, phase: .cancelled))
          } else {
            continuation.yield(
              TranscriptionEvent(
                type: .completed,
                completion: TranscriptionCompletion(
                  segmentCount: segments,
                  language: request.language,
                  durationMS: request.sourceDurationMS)))
          }
          continuation.finish()
        } catch {
          continuation.finish(throwing: error)
        }
      }
      continuation.onTermination = { _ in task.cancel() }
    }
  }

  private func waitIfPaused() async {
    guard paused else { return }
    pausedContinuation.yield()
    await withCheckedContinuation { continuation in
      resumeWaiters.append(continuation)
    }
  }
}
