import ArgumentParser
import SymMeetCore

extension SymMeet {
  struct Capabilities: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "capabilities",
      abstract: "Print machine-readable capabilities for cross-repository integration."
    )

    @Flag(name: .long, help: "Emit one machine-readable capabilities document.")
    var json = false

    mutating func run() throws {
      if json {
        let output = CapabilitiesOutput(
          tool: "symmeet",
          version: BuildInfo.version,
          schemaVersion: SymMeetCore.schemaVersion,
          artifactSchemaVersions: [SymMeetCore.schemaVersion],
          mcp: MCPInfo(command: "symmeet mcp", transport: "stdio"),
          exportFormats: ExportFormat.allCases.map(\.rawValue),
          models: modelList()
        )
        try Output.writeJSON(output)
      } else {
        Output.writeLine("symmeet \(BuildInfo.version) (schema \(SymMeetCore.schemaVersion))")
        Output.writeLine("Export formats: \(ExportFormat.allCases.map(\.rawValue).joined(separator: ", "))")
        Output.writeLine("MCP: symmeet mcp (stdio)")
      }
    }

    private func modelList() -> [ModelInfo] {
      let catalog = ModelCatalog.beta
      return catalog.descriptors.map { desc in
        ModelInfo(
          id: desc.id,
          engineID: desc.engineID,
          supportedArchitectures: desc.supportedArchitectures
        )
      }
    }
  }
}

private struct CapabilitiesOutput: Encodable {
  let tool: String
  let version: String
  let schemaVersion: Int
  let artifactSchemaVersions: [Int]
  let mcp: MCPInfo
  let exportFormats: [String]
  let models: [ModelInfo]

  private enum CodingKeys: String, CodingKey {
    case tool, version
    case schemaVersion = "schema_version"
    case artifactSchemaVersions = "artifact_schema_versions"
    case mcp, exportFormats = "export_formats"
    case models
  }
}

private struct MCPInfo: Encodable {
  let command: String
  let transport: String
}

private struct ModelInfo: Encodable {
  let id: String
  let engineID: String
  let supportedArchitectures: [String]

  private enum CodingKeys: String, CodingKey {
    case id
    case engineID = "engine_id"
    case supportedArchitectures = "supported_architectures"
  }
}
