import ArgumentParser
import SymMeetCore

extension SymMeet {
  struct Configuration: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "config",
      abstract: "Inspect local configuration.",
      subcommands: [Path.self]
    )
  }

  struct Path: AsyncParsableCommand {
    static let configuration = CommandConfiguration(abstract: "Print the configuration file path.")

    @Flag(name: .long, help: "Emit one machine-readable path document.")
    var json = false

    mutating func run() async throws {
      let path = SymMeetPaths().configFile.path
      if json {
        try Output.writeJSON(ConfigPathOutput(configPath: path))
      } else {
        Output.writeLine(path)
      }
    }
  }
}

private struct ConfigPathOutput: Encodable {
  let configPath: String
}
