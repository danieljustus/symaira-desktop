import ArgumentParser
import Foundation
import SymMeetCore

extension SymMeet {
  struct Diarize: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "diarize",
      abstract: "Diarize a completed meeting transcript to identify speakers."
    )

    @Argument(help: "The meeting UUID.")
    var meetingID: String

    @Option(
      name: .long,
      help: "Speaker count hint: 'auto' for engine estimation, or an integer count."
    )
    var speakers: String = "auto"

    @Flag(name: .long, help: "Emit one machine-readable result document to stdout.")
    var json = false

    mutating func run() async throws {
      let paths = SymMeetPaths()
      let store = MeetingStore(dataRoot: paths.dataDirectory)

      let normalizedID: String
      do {
        let manifest = try await store.load(meetingID: meetingID)
        normalizedID = manifest.meetingID.uuidString.lowercased()
      } catch {
        throw CLIError.from(error, isJSON: json)
      }

      let numberOfSpeakers: Int?
      if speakers == "auto" {
        numberOfSpeakers = nil
      } else if let count = Int(speakers), count >= 1 {
        numberOfSpeakers = count
      } else {
        throw CLIError(
          exitCode: CLIExit.usage.rawValue, code: "invalid_speakers",
          message: "Invalid --speakers value '\(speakers)'. Use 'auto' or a positive integer.",
          isJSON: json)
      }

      let pipeline = PostRecordingPipeline(dataRoot: paths.dataDirectory)
      let meetingUUID = UUID(uuidString: normalizedID) ?? UUID()

      do {
        let outcome = try await pipeline.run(
          meetingID: meetingUUID,
          numberOfSpeakers: numberOfSpeakers,
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
            case .alignmentSegments:
              break
            case .exportWritten:
              break
            }
          })

        if json {
          try Output.writeJSON(DiarizeOutput(outcome: outcome))
        } else {
          Output.writeLine("Diarization: \(outcome.diarizationTurns) turn(s)")
          Output.writeLine("Meeting: \(normalizedID)")
          Output.writeLine("State: \(outcome.state.status(of: .diarization).rawValue)")
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }
}

private struct DiarizeOutput: Encodable {
  let meetingID: String
  let diarizationTurns: Int
  let alignmentSegments: Int
  let state: String

  init(outcome: PostRecordingOutcome) {
    meetingID = outcome.meetingID.uuidString.lowercased()
    diarizationTurns = outcome.diarizationTurns
    alignmentSegments = outcome.alignmentSegments
    state = outcome.state.status(of: .diarization).rawValue
  }
}
