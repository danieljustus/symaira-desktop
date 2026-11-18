import Foundation

/// Optional last stage of the pipeline, typically an on-device language model that fixes
/// repetitions, self-corrections or formatting.
///
/// It is a protocol so the shared core stays free of model dependencies; the Mac agent and the
/// iPhone app inject their own implementation.
public protocol DictationPostProcessor: Sendable {
  /// Returns the polished text for `text` (already corrected and de-filled).
  func process(_ text: String, context: DictationContext) async throws -> String
}

/// Runs a post-processor with a deadline.
///
/// A slow or broken model must never delay or lose a dictation, so any failure (thrown error,
/// timeout, empty answer) is reported as `nil` and the caller keeps the rule-based text. The
/// deadline is enforced by racing two unstructured tasks; a structured task group would wait
/// for a processor that ignores cancellation and defeat the timeout.
enum DictationPostProcessing {
  static func run(
    _ processor: any DictationPostProcessor,
    on text: String,
    context: DictationContext,
    timeout: TimeInterval
  ) async -> String? {
    let output: String? = await withCheckedContinuation { continuation in
      let gate = ResumeGate(continuation)
      let work = Task {
        do {
          gate.finish(with: try await processor.process(text, context: context))
        } catch {
          gate.finish(with: nil)
        }
      }
      let timer = Task {
        try? await Task.sleep(nanoseconds: UInt64(max(0, timeout) * 1_000_000_000))
        gate.finish(with: nil)
      }
      gate.attach { work.cancel() }
      gate.attach { timer.cancel() }
    }
    guard let output, !output.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
      return nil
    }
    return output
  }
}

/// Resumes a continuation exactly once and cancels the racing tasks afterwards.
private final class ResumeGate: @unchecked Sendable {
  private let lock = NSLock()
  private var continuation: CheckedContinuation<String?, Never>?
  private var cancellers: [@Sendable () -> Void] = []
  private var finished = false

  init(_ continuation: CheckedContinuation<String?, Never>) {
    self.continuation = continuation
  }

  func finish(with value: String?) {
    lock.lock()
    guard !finished else {
      lock.unlock()
      return
    }
    finished = true
    let pending = continuation
    continuation = nil
    let toCancel = cancellers
    cancellers = []
    lock.unlock()
    pending?.resume(returning: value)
    for cancel in toCancel { cancel() }
  }

  /// Registers a cancel action; runs it immediately if the race is already decided.
  func attach(_ cancel: @escaping @Sendable () -> Void) {
    lock.lock()
    if finished {
      lock.unlock()
      cancel()
    } else {
      cancellers.append(cancel)
      lock.unlock()
    }
  }
}
