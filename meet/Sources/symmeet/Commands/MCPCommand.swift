import ArgumentParser
import SymMeetMCP

extension SymMeet {
  struct MCP: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "mcp",
      abstract:
        "Run the MCP stdio server for agent integration. Local stdio only — no network transport."
    )

    mutating func run() async throws {
      let server = MCPServer()
      try await server.run()
    }
  }
}
