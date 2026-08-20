import ArgumentParser
import Foundation
import SymMeetCapture

extension SymMeet {
  /// `symmeet capture`
  struct Capture: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "capture",
      abstract: "Inspect and manage audio capture sources.",
      subcommands: [SourcesCommand.self]
    )
  }
}

extension SymMeet.Capture {
  // MARK: - symmeet capture sources

  struct SourcesCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "sources",
      abstract: "List available audio sources for capture."
    )

    @Flag(name: .long, help: "Emit one machine-readable JSON document.")
    var json = false

    mutating func run() async throws {
      let service = CaptureSourceService()

      let list: CaptureSourceList
      do {
        list = try await service.availableSources()
      } catch let err as CaptureError {
        Output.writeError(err.localizedDescription)
        throw ExitCode(1)
      }

      if json {
        try Output.writeJSON(CaptureSourcesOutput(list: list))
      } else {
        if list.displays.isEmpty && list.applications.isEmpty && list.microphones.isEmpty {
          Output.writeLine("No audio sources found. Grant Screen Recording permission first.")
          return
        }
        if !list.displays.isEmpty {
          Output.writeLine("Displays:")
          for d in list.displays {
            Output.writeLine("  \(d.id)  \(d.displayName)")
          }
        }
        if !list.applications.isEmpty {
          Output.writeLine("Applications:")
          for app in list.applications {
            let active = app.isActive ? "" : " (not on screen)"
            Output.writeLine("  \(app.id)  \(app.displayName)\(active)")
          }
        }
        if !list.microphones.isEmpty {
          Output.writeLine("Microphones:")
          for mic in list.microphones {
            Output.writeLine("  \(mic.id)  \(mic.displayName)")
          }
        }
      }
    }
  }
}

// MARK: - Output model

private struct CaptureSourcesOutput: Encodable {
  let displays: [CaptureSourceEntry]
  let applications: [CaptureSourceEntry]
  let microphones: [CaptureSourceEntry]

  init(list: CaptureSourceList) {
    displays = list.displays.map(CaptureSourceEntry.init)
    applications = list.applications.map(CaptureSourceEntry.init)
    microphones = list.microphones.map(CaptureSourceEntry.init)
  }
}

private struct CaptureSourceEntry: Encodable {
  let id: String
  let kind: String
  let displayName: String
  let bundleID: String?
  let isActive: Bool
  let supportsSystemAudio: Bool

  init(source: CaptureSource) {
    id = source.id
    kind = source.kind.rawValue
    displayName = source.displayName
    bundleID = source.bundleID
    isActive = source.isActive
    supportsSystemAudio = source.supportsSystemAudio
  }
}
