import ArgumentParser

extension SymMeet {
  struct Completion: AsyncParsableCommand {
    static let configuration = CommandConfiguration(abstract: "Generate a shell completion script.")

    @Argument(help: "One of: bash, fish, zsh.")
    var shell: String

    mutating func run() async throws {
      guard let completionShell = CompletionShell(rawValue: shell) else {
        throw CLIError.unsupported("Unsupported shell. Choose bash, fish, or zsh.")
      }
      Output.writeRaw(SymMeet.completionScript(for: completionShell))
    }
  }
}
