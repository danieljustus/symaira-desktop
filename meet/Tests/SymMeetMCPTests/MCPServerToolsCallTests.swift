import Foundation
import SymairaMCP
import XCTest

@testable import SymMeetMCP

final class MCPServerToolsCallTests: XCTestCase {

  func testToolsCallRunsMeetingListThroughServer() async throws {
    let root = try makeTemporaryDirectory()
    defer { try? FileManager.default.removeItem(at: root) }
    let harness = MCPPipeHarness(
      server: MeetMCPServer(agentBridge: LocalAgentBridge(), dataRoot: root))
    defer { try? harness.clientWrite.close() }

    try harness.send(
      #"{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"meeting_list","arguments":{}}}"#
    )
    let envelope = try await harness.nextResponse()
    XCTAssertEqual(envelope.id, .number(1))
    XCTAssertNil(envelope.error)

    let result = try XCTUnwrap(try decodeResult(envelope, as: MCPCallToolResult.self))
    XCTAssertEqual(result.isError, false)
    let text = try XCTUnwrap(result.content.first?.text)
    XCTAssertTrue(text.contains("\"meetings\""))
    XCTAssertTrue(text.contains("\"diagnostics\""))
  }

  func testToolsCallUnknownToolReturnsToolError() async throws {
    let root = try makeTemporaryDirectory()
    defer { try? FileManager.default.removeItem(at: root) }
    let harness = MCPPipeHarness(server: MeetMCPServer(dataRoot: root))
    defer { try? harness.clientWrite.close() }

    try harness.send(
      #"{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}"#
    )
    let envelope = try await harness.nextResponse()
    XCTAssertEqual(envelope.id, .number(2))
    XCTAssertNil(envelope.result)
    XCTAssertEqual(envelope.error?.code, -32603)
    XCTAssertTrue(envelope.error?.message.contains("Unknown tool: no_such_tool") ?? false)
  }
}
