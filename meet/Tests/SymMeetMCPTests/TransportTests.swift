import SymairaMCP
import XCTest

@testable import SymMeetMCP

final class TransportTests: XCTestCase {

  // MARK: - Stdio framing (newline-delimited JSON)

  func testResponseFrameIsNewlineDelimited() async throws {
    let root = try makeTemporaryDirectory()
    defer { try? FileManager.default.removeItem(at: root) }
    let harness = MCPPipeHarness(server: MeetMCPServer(dataRoot: root))
    defer { try? harness.clientWrite.close() }

    try harness.send(#"{"jsonrpc":"2.0","id":7,"method":"ping"}"#)
    let maybeLine = try await harness.nextLine()
    let line = try XCTUnwrap(maybeLine)

    // A response is exactly one line: no raw newline inside the frame.
    XCTAssertFalse(line.contains("\n"), "Frame must contain no embedded newlines")
    let envelope = try JSONDecoder().decode(ResponseEnvelope.self, from: Data(line.utf8))
    XCTAssertEqual(envelope.id, .number(7))
    XCTAssertEqual(envelope.result, .object([:]))
  }

  func testFrameKeepsMultiLineTextOnSingleLine() async throws {
    // Text containing newlines must be escaped by the encoder so the frame
    // stays one line — a raw newline inside a message would break
    // newline-delimited framing.
    let root = try makeTemporaryDirectory()
    defer { try? FileManager.default.removeItem(at: root) }
    let harness = MCPPipeHarness(server: MeetMCPServer(dataRoot: root))
    defer { try? harness.clientWrite.close() }

    try harness.send(
      #"{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"meeting_list","arguments":{}}}"#
    )
    let maybeLine = try await harness.nextLine()
    let line = try XCTUnwrap(maybeLine)
    XCTAssertFalse(line.contains("\n"), "Frame must contain no embedded newlines")

    // The JSON payload round-trips, and its text content may itself contain
    // escaped newlines.
    let envelope = try JSONDecoder().decode(ResponseEnvelope.self, from: Data(line.utf8))
    XCTAssertEqual(envelope.id, .number(8))
    XCTAssertNil(envelope.error)
  }

  // MARK: - Server handles messages

  func testServerInitialize() async throws {
    let harness = MCPPipeHarness(server: MeetMCPServer())
    defer { try? harness.clientWrite.close() }

    try harness.send(
      #"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0"}}}"#
    )
    let envelope = try await harness.nextResponse()
    XCTAssertEqual(envelope.id, .number(1))
    XCTAssertNil(envelope.error)

    let result = try XCTUnwrap(try decodeResult(envelope, as: MCPInitializeResult.self))
    XCTAssertEqual(result.protocolVersion, "2024-11-05")
    XCTAssertEqual(result.serverInfo.name, "symmeet")
    XCTAssertEqual(result.serverInfo.version, "0.1.0")
    if case .object(let tools) = result.capabilities["tools"] {
      XCTAssertEqual(tools["listChanged"], .bool(false))
    } else {
      XCTFail(
        "Expected a tools capability, got \(String(describing: result.capabilities["tools"]))")
    }
  }

  func testServerToolsList() async throws {
    let harness = MCPPipeHarness(server: MeetMCPServer())
    defer { try? harness.clientWrite.close() }

    try harness.send(#"{"jsonrpc":"2.0","id":2,"method":"tools/list"}"#)
    let envelope = try await harness.nextResponse()
    XCTAssertEqual(envelope.id, .number(2))
    XCTAssertNil(envelope.error)

    let result = try XCTUnwrap(envelope.result)
    let tools = try XCTUnwrap(result.objectValue?["tools"]?.arrayValue)
    XCTAssertEqual(tools.count, 9)
    XCTAssertEqual(tools.first?.objectValue?["name"]?.stringValue, "meeting_list")
  }

  func testServerPing() async throws {
    let harness = MCPPipeHarness(server: MeetMCPServer())
    defer { try? harness.clientWrite.close() }

    try harness.send(#"{"jsonrpc":"2.0","id":3,"method":"ping"}"#)
    let envelope = try await harness.nextResponse()
    XCTAssertEqual(envelope.id, .number(3))
    XCTAssertNil(envelope.error)
    XCTAssertEqual(envelope.result, .object([:]))
  }

  func testServerUnknownMethod() async throws {
    let harness = MCPPipeHarness(server: MeetMCPServer())
    defer { try? harness.clientWrite.close() }

    try harness.send(#"{"jsonrpc":"2.0","id":4,"method":"unknown/method"}"#)
    let envelope = try await harness.nextResponse()
    XCTAssertEqual(envelope.id, .number(4))
    XCTAssertNil(envelope.result)
    XCTAssertEqual(envelope.error?.code, -32601)
    XCTAssertTrue(envelope.error?.message.contains("unknown/method") ?? false)
  }

  func testServerUnknownTool() async throws {
    let harness = MCPPipeHarness(server: MeetMCPServer())
    defer { try? harness.clientWrite.close() }

    try harness.send(
      #"{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nonexistent_tool","arguments":{}}}"#
    )
    let envelope = try await harness.nextResponse()
    XCTAssertEqual(envelope.id, .number(5))
    XCTAssertNil(envelope.result)
    XCTAssertEqual(envelope.error?.code, -32603)
    XCTAssertTrue(envelope.error?.message.contains("Unknown tool") ?? false)
  }

  func testServerMeetingListUsesInjectedTemporaryDataRoot() async throws {
    let root = FileManager.default.temporaryDirectory
      .appendingPathComponent("symmeet-mcp-tests-\(UUID().uuidString)", isDirectory: true)
    defer { try? FileManager.default.removeItem(at: root) }

    let harness = MCPPipeHarness(server: MeetMCPServer(dataRoot: root))
    defer { try? harness.clientWrite.close() }

    try harness.send(
      #"{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"meeting_list","arguments":{}}}"#
    )
    let envelope = try await harness.nextResponse()
    XCTAssertEqual(envelope.id, .number(6))
    XCTAssertNil(envelope.error)
    XCTAssertTrue(
      FileManager.default.fileExists(atPath: root.appendingPathComponent("meetings").path),
      "The handler must use the server's injected temporary data root"
    )
  }

  // MARK: - MCPToolResult

  func testToolResultText() {
    let result = MCPToolResult.text("hello world")
    XCTAssertEqual(result.content.count, 1)
    XCTAssertEqual(result.content[0].type, "text")
    XCTAssertEqual(result.content[0].text, "hello world")
    XCTAssertFalse(result.isError)
  }

  func testToolResultError() {
    let result = MCPToolResult.error("something went wrong")
    XCTAssertEqual(result.content.count, 1)
    XCTAssertEqual(result.content[0].type, "text")
    XCTAssertTrue(result.isError)
  }
}
