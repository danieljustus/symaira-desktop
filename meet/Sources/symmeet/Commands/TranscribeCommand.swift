import ArgumentParser
import Foundation
import SymMeetCore
import SymMeetWhisperKit

extension SymMeet {
  struct Transcribe: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "transcribe",
      abstract: "Transcribe a local audio or video file into a meeting artifact."
    )

    @Argument(help: "Path to the audio or video file to transcribe.")
    var file: String

    @Option(name: .long, help: "Title for the meeting artifact.")
    var title: String?

    @Option(name: .long, help: "Language code (e.g. 'en', 'de') or 'auto' for detection.")
    var language: String = "auto"

    @Option(name: .long, help: "Catalog model identifier to use for transcription.")
    var model: String = "tiny"

    @Flag(name: .long, help: "Emit one machine-readable result document to stdout.")
    var json = false

    mutating func run() async throws {
      let paths = SymMeetPaths()
      let sourceURL = URL(fileURLWithPath: file)

      guard FileManager.default.fileExists(atPath: sourceURL.path) else {
        throw CLIError(
          exitCode: CLIExit.usage.rawValue, code: "file_not_found",
          message: "File not found: \(file)", isJSON: json)
      }

      let modelStore = ModelStore()
      let record: ModelRecord
      do {
        record = try await modelStore.verify(id: model)
      } catch ModelError.modelNotInstalled {
        throw CLIError(
          exitCode: CLIExit.runtimeFailure.rawValue, code: "model_not_installed",
          message:
            "Model '\(model)' is not installed. Run: symmeet model install \(model)",
          isJSON: json)
      } catch {
        throw CLIError.from(error, isJSON: json)
      }

      let languageValue = language == "auto" ? nil : language
      let pipeline = TranscriptionPipeline(dataRoot: paths.dataDirectory)

      let engine: WhisperKitEngine
      do {
        engine = try await WhisperKitEngine(modelID: model, modelStore: modelStore)
      } catch {
        throw CLIError.from(error, isJSON: json)
      }

      // Cooperative SIGINT handling: request cancellation, do not terminate
      // immediately so durable state is reached.
      let signalSource = DispatchSource.makeSignalSource(signal: SIGINT)
      signal(SIGINT, SIG_IGN)
      signalSource.setEventHandler(handler: {
        Task { @Sendable in await pipeline.requestCancellation() }
      })
      signalSource.resume()

      defer { signalSource.cancel() }

      let tracker = ProgressTracker()
      do {
        let outcome = try await pipeline.run(
          TranscriptionRequestOptions(
            sourceURL: sourceURL,
            title: title,
            language: languageValue,
            modelID: record.descriptor.id,
            modelVersion: record.descriptor.upstreamRevision,
            engineID: record.descriptor.engineID
          ),
          engine: engine,
          onProgress: { progress in
            switch progress {
            case .meetingCreated(let id):
              Output.writeError("Meeting \(id.uuidString.lowercased()) created.")
            case .phase(let phase):
              Output.writeError("Phase: \(phase.rawValue)")
            case .progress(let value):
              tracker.update(value)
            case .segmentFinalized:
              break
            case .warning(let warning):
              Output.writeError("Warning [\(warning.code)]: \(warning.message)")
            }
          })

        if json {
          try Output.writeJSON(TranscribeOutput(outcome: outcome))
        } else {
          Output.writeLine(
            "Transcribed \(outcome.segmentCount) segment(s) (\(outcome.language ?? "auto")).")
          Output.writeLine("Meeting: \(outcome.meetingID.uuidString.lowercased())")
          Output.writeLine("Status: \(outcome.status.rawValue)")
        }
      } catch PipelineError.engineFailed(let message) {
        throw CLIError(
          exitCode: CLIExit.runtimeFailure.rawValue, code: "engine_failed",
          message: message, isJSON: json)
      } catch PipelineError.engineProducedNoCompletion {
        throw CLIError(
          exitCode: CLIExit.runtimeFailure.rawValue,
          code: "engine_produced_no_completion",
          message: PipelineError.engineProducedNoCompletion.localizedDescription,
          isJSON: json)
      } catch PipelineError.missingOriginalAsset {
        throw CLIError(
          exitCode: CLIExit.runtimeFailure.rawValue,
          code: "missing_original_asset",
          message: PipelineError.missingOriginalAsset.localizedDescription,
          isJSON: json)
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }
}

/// Thread-safe progress tracker for the @Sendable onProgress closure.
private final class ProgressTracker: @unchecked Sendable {
  private let lock = NSLock()
  private var lastPercent = -1

  func update(_ value: Double) {
    let percent = Int(value * 100)
    lock.lock()
    let shouldEmit = percent > lastPercent
    if shouldEmit { lastPercent = percent }
    lock.unlock()
    if shouldEmit {
      Output.writeError("Progress: \(percent)%")
    }
  }
}

private struct TranscribeOutput: Encodable {
  let meetingID: String
  let jobID: String
  let state: String
  let attempt: Int
  let segmentCount: Int
  let language: String?
  let engineID: String
  let modelID: String
  let sourceHash: String

  init(outcome: TranscriptionOutcome) {
    meetingID = outcome.meetingID.uuidString.lowercased()
    jobID = outcome.jobID.uuidString.lowercased()
    state = outcome.status.rawValue
    attempt = outcome.attempt
    segmentCount = outcome.segmentCount
    language = outcome.language
    engineID = outcome.engineID
    modelID = outcome.modelID
    sourceHash = outcome.sourceHash
  }
}
