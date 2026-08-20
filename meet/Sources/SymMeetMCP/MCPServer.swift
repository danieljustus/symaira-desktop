import Foundation
import SymMeetCore
import SymairaMCP

// MARK: - MCP Server

/// The MCP server that handles JSON-RPC 2.0 initialize/list/call lifecycle
/// over stdio. Stdout contains only valid JSON-RPC frames; logs and
/// diagnostics go to stderr.
///
/// The wire protocol, transport, and dispatch are provided by appkit's
/// `SymairaMCP` module (`MCPServer` + `MCPStdioTransport`); this type
/// registers the app's tool handlers on top of the typed
/// `withMethodHandler` dispatcher and adapts their results.
public struct MCPServer: Sendable {
  let agentBridge: AgentBridge

  /// All registered tool handlers, keyed by tool name.
  private let handlers: [String: MCPToolHandler]

  public init(
    agentBridge: AgentBridge = LocalAgentBridge(),
    dataRoot: URL = SymMeetPaths().dataDirectory
  ) {
    self.agentBridge = agentBridge
    let store = MeetingStore(dataRoot: dataRoot)

    var handlerMap: [String: MCPToolHandler] = [:]
    let allHandlers: [MCPToolHandler] = [
      MeetingListHandler(store: store),
      MeetingGetHandler(store: store),
      MeetingTranscribeHandler(store: store, dataRoot: dataRoot),
      MeetingJobStatusHandler(store: store, dataRoot: dataRoot),
      MeetingJobCancelHandler(store: store, dataRoot: dataRoot),
      MeetingExportHandler(store: store),
      MeetingRecordingStatusHandler(agentBridge: agentBridge),
      MeetingRecordingRequestHandler(agentBridge: agentBridge),
      MeetingRecordingStopHandler(agentBridge: agentBridge),
    ]
    for handler in allHandlers {
      handlerMap[handler.toolName] = handler
    }
    self.handlers = handlerMap
  }

  // MARK: - Server lifecycle

  /// Runs the MCP server, reading JSON-RPC messages from the transport's
  /// input and writing responses to its output. Runs until the input closes
  /// (e.g. stdin EOF) or `stop()` is called.
  ///
  /// Framing rule (MCP stdio spec): newline-delimited JSON — each message is
  /// exactly one line. Stdout carries protocol frames only; diagnostics go
  /// to stderr.
  public func run(transport: any MCPTransport = MCPStdioTransport()) async throws {
    let server = SymairaMCP.MCPServer(
      name: "symmeet",
      version: "0.1.0",
      protocolVersion: "2024-11-05"
    )
    .withMethodHandler("tools/list") { (_: MCPNoParams) async throws -> ToolListResult in
      ToolListResult(tools: MCPToolRegistry.tools)
    }
    .withMethodHandler("tools/call") {
      (params: MCPCallToolParams) async throws -> MCPCallToolResult in
      try await self.handleToolsCall(params)
    }

    log("symmeet MCP server starting (schema \(SymMeetMCP.protocolSchemaVersion))")
    try await server.start(transport: transport)
    log("symmeet MCP server shutting down")
  }

  // MARK: - Tools call

  private func handleToolsCall(_ params: MCPCallToolParams) async throws -> MCPCallToolResult {
    guard let handler = handlers[params.name] else {
      throw MCPError("Unknown tool: \(params.name)")
    }

    let arguments: [String: AnyCodable] = (params.arguments ?? [:]).mapValues { value in
      AnyCodable(Self.anyValue(from: value))
    }

    do {
      let result = try await handler.execute(args: arguments)
      return MCPCallToolResult(
        content: result.content.map { MCPTextContent(type: $0.type, text: $0.text ?? "") },
        isError: result.isError
      )
    } catch {
      throw MCPError(error.localizedDescription)
    }
  }

  // MARK: - JSON value bridging

  /// Converts an appkit MCP JSON value to the plain `Any` representation the
  /// app-owned tool handlers consume. Integral numbers stay `Int` so the
  /// handlers' `asInt` accessors keep working.
  private static func anyValue(from value: MCPJSONValue) -> Any {
    switch value {
    case .null:
      return NSNull()
    case .bool(let bool):
      return bool
    case .number(let double):
      if let int = value.intValue {
        return Int(int)
      }
      return double
    case .string(let string):
      return string
    case .array(let values):
      return values.map { anyValue(from: $0) }
    case .object(let object):
      return object.mapValues { anyValue(from: $0) }
    }
  }
}

// MARK: - Tool list result

/// The wire payload of `tools/list`: the app's tool schemas in their
/// established encoding (including `input_schema` and property `default`s),
/// unchanged by the migration.
struct ToolListResult: Encodable, Sendable {
  let tools: [MCPToolSchema]
}

// MARK: - Diagnostics

private func log(_ message: String) {
  FileHandle.standardError.write(Data(("[symmeet-mcp] " + message + "\n").utf8))
}
