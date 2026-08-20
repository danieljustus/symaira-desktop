import ArgumentParser
import Foundation
import SymMeetCore
import SymMeetWhisperKit

extension SymMeet {
  struct Benchmark: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "benchmark",
      abstract: "Benchmark transcription performance on a local file."
    )

    @Argument(help: "Path to the audio or video file to benchmark.")
    var file: String

    @Option(name: .long, help: "Catalog model identifier to use.")
    var model: String = "tiny"

    @Flag(name: .long, help: "Emit machine-readable JSON summary.")
    var json = false

    mutating func run() async throws {
      guard FileManager.default.fileExists(atPath: file) else {
        throw CLIError(
          exitCode: CLIExit.usage.rawValue,
          code: "file_not_found",
          message: "File not found: \(file)",
          isJSON: json)
      }

      let modelStore = ModelStore()
      let record: ModelRecord
      do {
        record = try await modelStore.verify(id: model)
      } catch ModelError.modelNotInstalled {
        throw CLIError(
          exitCode: CLIExit.usage.rawValue,
          code: "model_not_installed",
          message: "Model '\(model)' is not installed. Run: symmeet model install \(model)",
          isJSON: json)
      } catch {
        throw CLIError.from(error, isJSON: json)
      }

      let startTime = Date()
      let paths = SymMeetPaths()
      let pipeline = TranscriptionPipeline(dataRoot: paths.dataDirectory)
      let sourceURL = URL(fileURLWithPath: file)

      let engine = try await WhisperKitEngine(
        modelID: model, modelStore: modelStore)

      let outcome = try await pipeline.run(
        TranscriptionRequestOptions(
          sourceURL: sourceURL,
          title: nil,
          language: nil,
          modelID: record.descriptor.id,
          modelVersion: record.descriptor.upstreamRevision,
          engineID: record.descriptor.engineID
        ),
        engine: engine
      )

      let elapsed = Date().timeIntervalSince(startTime)
      let durationMS = elapsed * 1000
      let rtf = elapsed > 0 ? elapsed / max(durationMS / 1000, 0.001) : 0

      if json {
        let output = BenchmarkOutput(
          model: model,
          file: file,
          segmentCount: outcome.segmentCount,
          language: outcome.language,
          wallTimeSeconds: elapsed,
          realTimeFactor: rtf,
          engineID: outcome.engineID
        )
        try Output.writeJSON(output)
      } else {
        Output.writeLine("Model: \(model)")
        Output.writeLine("Segments: \(outcome.segmentCount)")
        Output.writeLine("Language: \(outcome.language ?? "auto")")
        Output.writeLine(String(format: "Time: %.2fs", elapsed))
        Output.writeLine(String(format: "RTF: %.4f", rtf))
      }
    }
  }
}

private struct BenchmarkOutput: Encodable {
  let model: String
  let file: String
  let segmentCount: Int
  let language: String?
  let wallTimeSeconds: TimeInterval
  let realTimeFactor: Double
  let engineID: String

  private enum CodingKeys: String, CodingKey {
    case model, file
    case segmentCount = "segment_count"
    case language
    case wallTimeSeconds = "wall_time_seconds"
    case realTimeFactor = "real_time_factor"
    case engineID = "engine_id"
  }
}
