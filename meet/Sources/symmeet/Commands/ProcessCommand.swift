import ArgumentParser
import Foundation
import SymMeetCore

extension SymMeet {
  struct Process: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "process",
      abstract: "Run the post-recording pipeline on an existing meeting."
    )

    @Argument(help: "The meeting UUID.")
    var meetingID: String

    @Flag(name: .long, help: "Include transcription in the pipeline run.")
    var transcribe = false

    @Flag(name: .long, help: "Include diarization in the pipeline run.")
    var diarize = false

    @Flag(name: .long, help: "Re-run diarization even if already completed.")
    var forceRediarize = false

    @Option(
      name: .long,
      help: "Speaker count hint for diarization: 'auto' or an integer."
    )
    var speakers: String = "auto"

    @Flag(name: .long, help: "Emit one machine-readable result document to stdout.")
    var json = false

    mutating func run() async throws {
      let paths = SymMeetPaths()
      let store = MeetingStore(dataRoot: paths.dataDirectory)

      let manifest: MeetingManifest
      do {
        manifest = try await store.load(meetingID: meetingID)
      } catch {
        throw CLIError.from(error, isJSON: json)
      }

      let normalizedID = manifest.meetingID.uuidString.lowercased()
      let meetingUUID = manifest.meetingID

      let numberOfSpeakers: Int?
      if speakers == "auto" {
        numberOfSpeakers = nil
      } else if let count = Int(speakers), count >= 1 {
        numberOfSpeakers = count
      } else {
        throw CLIError(
          exitCode: CLIExit.usage.rawValue,
          code: "invalid_speakers",
          message: "Invalid --speakers value '\(speakers)'. Use 'auto' or a positive integer.",
          isJSON: json)
      }

      let pipeline = PostRecordingPipeline(dataRoot: paths.dataDirectory)

      do {
        let outcome = try await pipeline.run(
          meetingID: meetingUUID,
          numberOfSpeakers: numberOfSpeakers,
          forceRediarize: forceRediarize,
          onProgress: { progress in
            switch progress {
            case .phaseStarted(let phase):
              Output.writeError("Phase: \(phase.rawValue)")
            case .phaseSucceeded(let phase):
              Output.writeError("Completed: \(phase.rawValue)")
            case .phaseFailed(let phase, let message):
              Output.writeError("Failed [\(phase.rawValue)]: \(message)")
            case .diarizationTurns(let count):
              Output.writeError("Diarization: \(count) turn(s)")
            case .alignmentSegments(let count):
              Output.writeError("Alignment: \(count) segment(s)")
            case .exportWritten(let path):
              Output.writeError("Exported to: \(path)")
            }
          })

        if json {
          try Output.writeJSON(ProcessOutput(outcome: outcome))
        } else {
          let state = outcome.state
          Output.writeLine("Meeting: \(normalizedID)")
          Output.writeLine("Ready for review: \(state.isReadyForReview ? "yes" : "no")")
          Output.writeLine("Pipeline complete: \(state.isComplete ? "yes" : "no")")
          Output.writeLine(
            "Transcription: \(state.status(of: .transcription).rawValue)")
          Output.writeLine(
            "Diarization: \(state.status(of: .diarization).rawValue)")
          Output.writeLine(
            "Alignment: \(state.status(of: .alignment).rawValue)")
          Output.writeLine(
            "Projection: \(state.status(of: .projection).rawValue)")
          Output.writeLine("Export: \(state.status(of: .export).rawValue)")
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }
}

private struct ProcessOutput: Encodable {
  let meetingID: String
  let state: String
  let transcription: String
  let diarization: String
  let alignment: String
  let projection: String
  let export: String
  let diarizationTurns: Int
  let alignmentSegments: Int
  let editedSegments: Int

  init(outcome: PostRecordingOutcome) {
    meetingID = outcome.meetingID.uuidString.lowercased()
    state = outcome.state.isReadyForReview ? "ready_for_review" : "in_progress"
    transcription = outcome.state.status(of: .transcription).rawValue
    diarization = outcome.state.status(of: .diarization).rawValue
    alignment = outcome.state.status(of: .alignment).rawValue
    projection = outcome.state.status(of: .projection).rawValue
    `export` = outcome.state.status(of: .export).rawValue
    diarizationTurns = outcome.diarizationTurns
    alignmentSegments = outcome.alignmentSegments
    editedSegments = outcome.editedSegments
  }
}
