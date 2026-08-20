import ArgumentParser
import Foundation
import SymMeetCore
import SymairaUpdateCheck

extension SymMeet {
  struct Version: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      abstract: "Print the symmeet version.",
      subcommands: [Check.self]
    )

    @Flag(name: .long, help: "Emit the stable machine-readable version handshake.")
    var json = false

    mutating func run() async throws {
      if json {
        Output.writeRaw(
          "{\"tool\":\"symmeet\",\"version\":\"\(BuildInfo.version)\",\"schema_version\":\(SymMeetCore.schemaVersion)}\n"
        )
      } else {
        Output.writeLine("symmeet \(BuildInfo.version) (schema \(SymMeetCore.schemaVersion))")
      }
    }
  }
}

extension SymMeet.Version {
  struct Check: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "check",
      abstract: "Check if a newer symmeet release is available on GitHub."
    )

    @Flag(name: .long, help: "Bypass the disk cache and force a fresh check.")
    var force = false

    @Flag(name: .customLong("output-json"), help: "Emit machine-readable JSON to stdout.")
    var outputJSON = false

    mutating func run() async throws {
      let currentVersion = BuildInfo.version
      let checker = UpdateChecker(
        owner: "danieljustus",
        repo: "symaira-meet"
      )

      let release: ReleaseInfo?
      do {
        release = try await checker.check(
          currentVersion: currentVersion,
          force: force
        )
      } catch {
        if outputJSON {
          let output = VersionCheckOutput(
            currentVersion: currentVersion,
            newVersion: nil,
            url: nil,
            error: String(describing: error)
          )
          try Output.writeJSON(output)
        } else {
          Output.writeLine("symmeet \(currentVersion)")
          Output.writeError("Update check failed: \(error.localizedDescription)")
        }
        return
      }

      if outputJSON {
        let output = VersionCheckOutput(
          currentVersion: currentVersion,
          newVersion: release?.tagName,
          url: release?.htmlURL,
          error: nil
        )
        try Output.writeJSON(output)
      } else if let release = release {
        Output.writeLine("symmeet \(currentVersion)")
        Output.writeLine("")
        Output.writeLine("A newer release is available:")
        Output.writeLine("  \(release.tagName)")
        Output.writeLine("  \(release.htmlURL)")
        Output.writeLine("")
        Output.writeLine("Run 'brew upgrade symmeet' or download the latest release.")
      } else {
        Output.writeLine("symmeet \(currentVersion)")
        Output.writeLine("Up to date.")
      }
    }
  }
}

private struct VersionCheckOutput: Encodable {
  let currentVersion: String
  let newVersion: String?
  let url: String?
  let error: String?

  private enum CodingKeys: String, CodingKey {
    case currentVersion = "current_version"
    case newVersion = "new_version"
    case url
    case error
  }
}
