import Foundation
import SymairaMCP
import XCTest

@testable import SymMeetMCP

/// Parses the JSON payload carried by a text MCP tool result.
func jsonObject(_ result: MCPToolResult) throws -> [String: Any] {
  let text = try XCTUnwrap(result.content.first?.text)
  let data = try XCTUnwrap(text.data(using: .utf8))
  let object = try JSONSerialization.jsonObject(with: data)
  return try XCTUnwrap(object as? [String: Any])
}

func makeTemporaryDirectory() throws -> URL {
  let directory = FileManager.default.temporaryDirectory.appending(
    path: "symmeet-mcp-tests-\(UUID().uuidString)")
  try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
  return directory
}

/// Asserts that an async throwing expression throws.
/// Note: takes a plain closure (not @autoclosure) so trailing-closure call
/// sites actually execute the body; an @autoclosure would capture the
/// closure literal as a value and never observe the throw.
func assertThrowsAsync<T>(
  _ expression: () async throws -> T,
  file: StaticString = #filePath,
  line: UInt = #line
) async {
  do {
    _ = try await expression()
    XCTFail("Expected expression to throw", file: file, line: line)
  } catch {}
}

// MARK: - Stdio pipe harness

/// Reads one newline-delimited line from a handle. Test-local only: a single
/// iterator is created per handle so reads never interleave.
private final class LineReader: @unchecked Sendable {
  private var iterator: AsyncLineSequence<FileHandle.AsyncBytes>.AsyncIterator

  init(handle: FileHandle) {
    iterator = handle.bytes.lines.makeAsyncIterator()
  }

  func nextLine() async throws -> String? {
    try await iterator.next()
  }
}

enum MCPHarnessError: Error {
  case waitingForResponse
}

/// A JSON-RPC 2.0 response envelope decoded from a transport line.
struct ResponseEnvelope: Decodable {
  let jsonrpc: String?
  let id: MCPJSONRPCID?
  let result: MCPJSONValue?
  let error: MCPJSONRPCErrorObject?
}

/// Boots a SymMeetMCP server on an in-process pipe pair and lets the test act
/// as the MCP client over stdio framing (one JSON-RPC message per line).
struct MCPPipeHarness {
  let clientWrite: FileHandle
  private let reader: LineReader
  private let serverTask: Task<Void, Error>

  init(server: MeetMCPServer) {
    let clientToServer = Pipe()
    let serverToClient = Pipe()
    let transport = MCPStdioTransport(
      input: clientToServer.fileHandleForReading,
      output: serverToClient.fileHandleForWriting
    )
    // Detached: async XCTest methods run on a special executor that never
    // schedules unstructured tasks created inside the test body.
    self.serverTask = Task.detached { try await server.run(transport: transport) }
    self.clientWrite = clientToServer.fileHandleForWriting
    self.reader = LineReader(handle: serverToClient.fileHandleForReading)
  }

  /// Writes one newline-terminated JSON-RPC message to the server.
  func send(_ line: String) throws {
    var data = Data(line.utf8)
    data.append(0x0A)
    try clientWrite.write(contentsOf: data)
  }

  /// Reads the next response line, failing the test after `timeout` instead
  /// of hanging the suite if the server misbehaves.
  func nextLine(timeout: Duration = .seconds(10)) async throws -> String? {
    try await withThrowingTaskGroup(of: String?.self) { group in
      group.addTask { try await reader.nextLine() }
      group.addTask {
        try await Task.sleep(for: timeout)
        return nil
      }
      guard let first = try await group.next() else {
        throw MCPHarnessError.waitingForResponse
      }
      group.cancelAll()
      return first
    }
  }

  /// Reads the next response and decodes its JSON-RPC envelope.
  func nextResponse(timeout: Duration = .seconds(10)) async throws -> ResponseEnvelope {
    let maybeLine = try await nextLine(timeout: timeout)
    let line = try XCTUnwrap(maybeLine, "Expected a response line")
    return try JSONDecoder().decode(ResponseEnvelope.self, from: Data(line.utf8))
  }
}

/// Decodes the `result` payload of an envelope into a typed value.
func decodeResult<T: Decodable>(_ envelope: ResponseEnvelope, as type: T.Type) throws -> T? {
  guard let result = envelope.result else { return nil }
  let data = try JSONEncoder().encode(result)
  return try JSONDecoder().decode(type, from: data)
}
