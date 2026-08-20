@preconcurrency import ArgumentParser
import Darwin

@main
struct SymMeet: AsyncParsableCommand {
  static let configuration = CommandConfiguration(
    commandName: "symmeet",
    abstract: "Local-first meeting artifacts and processing.",
    subcommands: [
      Version.self, Doctor.self, Configuration.self, Meeting.self, Model.self,
      Transcribe.self, Job.self, Export.self, Completion.self,
      Permissions.self, Capture.self, Record.self, MCP.self,
      Capabilities.self, Benchmark.self,
      Diarize.self, Speaker.self, Process.self,
    ]
  )

  static func main() async {
    do {
      var command = try parseAsRoot()
      if var asyncCommand = command as? any AsyncParsableCommand {
        try await asyncCommand.run()
      } else {
        try command.run()
      }
    } catch let error as CLIError {
      if error.isJSON {
        let envelope = ErrorEnvelope(
          error: .init(code: error.code, message: error.message))
        try? Output.writeJSON(envelope)
      } else {
        Output.writeError(error.message)
      }
      Darwin.exit(error.exitCode)
    } catch {
      // Covers CleanExit (--help/--version, always a clean exit) and
      // ParserError (usage mistakes, e.g. --help also surfaces as
      // .helpRequested here and must still exit cleanly). Message text comes
      // from ArgumentParser's own formatter; the usage exit code stays this
      // project's convention (CLIExit.usage) rather than ArgumentParser's
      // EX_USAGE default.
      let text = Self.fullMessage(for: error)
      if Self.exitCode(for: error).isSuccess {
        if !text.isEmpty { print(text) }
        Darwin.exit(CLIExit.success.rawValue)
      } else {
        if !text.isEmpty { Output.writeError(text) }
        Darwin.exit(CLIExit.usage.rawValue)
      }
    }
  }
}
