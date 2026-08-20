import ArgumentParser
import SymMeetCore

extension SymMeet {
  struct Meeting: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "meeting",
      abstract: "Inspect and manage meeting artifacts.",
      subcommands: [List.self, Show.self, Trash.self, Restore.self]
    )
  }

  struct List: AsyncParsableCommand {
    static let configuration = CommandConfiguration(abstract: "List portable meeting artifacts.")

    @Flag(name: .long, help: "Emit one machine-readable meeting list.")
    var json = false

    mutating func run() async throws {
      do {
        let result = try await MeetingStore().list()
        if json {
          try Output.writeJSON(MeetingListOutput(result))
        } else if result.meetings.isEmpty {
          Output.writeLine("No meetings found.")
        } else {
          for meeting in result.meetings {
            Output.writeLine(
              "\(meeting.meetingID.uuidString.lowercased())\t\(meeting.source.rawValue)")
          }
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  struct Show: AsyncParsableCommand {
    static let configuration = CommandConfiguration(abstract: "Show one meeting artifact.")

    @Argument(help: "The meeting UUID.")
    var meetingID: String

    @Flag(name: .long, help: "Emit one machine-readable meeting document.")
    var json = false

    mutating func run() async throws {
      do {
        let meeting = try await MeetingStore().load(meetingID: meetingID)
        if json {
          try Output.writeJSON(meeting)
        } else {
          Output.writeLine("Meeting: \(meeting.meetingID.uuidString.lowercased())")
          Output.writeLine("Source: \(meeting.source.rawValue)")
          Output.writeLine("Retention: \(meeting.retention.policy.rawValue)")
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  struct Trash: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      abstract: "Move one meeting artifact to local trash.")

    @Argument(help: "The meeting UUID.")
    var meetingID: String

    @Flag(name: .long, help: "Emit one machine-readable result document.")
    var json = false

    mutating func run() async throws {
      do {
        try await MeetingStore().trash(meetingID: meetingID)
        try writeMutationResult(status: "trashed")
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }

    private func writeMutationResult(status: String) throws {
      if json {
        try Output.writeJSON(
          MeetingMutationOutput(meetingID: meetingID.lowercased(), status: status))
      } else {
        Output.writeLine("\(status.capitalized) \(meetingID.lowercased()).")
      }
    }
  }

  struct Restore: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      abstract: "Restore one meeting artifact from local trash.")

    @Argument(help: "The meeting UUID.")
    var meetingID: String

    @Flag(name: .long, help: "Emit one machine-readable result document.")
    var json = false

    mutating func run() async throws {
      do {
        try await MeetingStore().restore(meetingID: meetingID)
        if json {
          try Output.writeJSON(
            MeetingMutationOutput(meetingID: meetingID.lowercased(), status: "restored"))
        } else {
          Output.writeLine("Restored \(meetingID.lowercased()).")
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  private struct MeetingListOutput: Encodable {
    let meetings: [MeetingManifest]
    let diagnostics: [StoreDiagnostic]

    init(_ result: MeetingList) {
      meetings = result.meetings
      diagnostics = result.diagnostics
    }
  }

  private struct MeetingMutationOutput: Encodable {
    let meetingID: String
    let status: String
  }
}
