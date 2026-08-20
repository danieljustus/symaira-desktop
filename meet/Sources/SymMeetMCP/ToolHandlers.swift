import Foundation
import SymMeetCore

// MARK: - Tool handler protocol

/// A handler for a single MCP tool.
protocol MCPToolHandler: Sendable {
  /// The tool name this handler serves.
  var toolName: String { get }

  /// Executes the tool with the given arguments. Returns the content to send back.
  func execute(args: [String: AnyCodable]) async throws -> MCPToolResult
}

// MARK: - Tool result

/// The result returned by a tool handler, conforming to MCP content format.
struct MCPToolResult: Sendable {
  let content: [MCPContent]
  let isError: Bool

  init(content: [MCPContent], isError: Bool = false) {
    self.content = content
    self.isError = isError
  }

  /// Convenience for a single text result.
  static func text(_ text: String, isError: Bool = false) -> MCPToolResult {
    MCPToolResult(content: [MCPContent(type: "text", text: text)], isError: isError)
  }

  /// Encodes one JSON result using the MCP output encoding contract.
  static func json<T: Encodable>(_ value: T) throws -> MCPToolResult {
    let data = try makeEncoder().encode(value)
    return .text(String(decoding: data, as: UTF8.self))
  }

  /// Convenience for an error result.
  static func error(_ message: String) -> MCPToolResult {
    .text(message, isError: true)
  }
}

/// A single content block in a tool result.
struct MCPContent: Codable, Sendable {
  let type: String
  let text: String?
}

// MARK: - JSON encoding helper

private func makeEncoder() -> JSONEncoder {
  let encoder = JSONEncoder()
  encoder.keyEncodingStrategy = .convertToSnakeCase
  encoder.outputFormatting = [.sortedKeys]
  return encoder
}

// MARK: - Shared helpers

func resolveJob(coordinator: JobCoordinator, identifier: String) async throws
  -> TranscriptionJob
{
  if let meetingID = UUID(uuidString: identifier) {
    do {
      return try await coordinator.load(meetingID: meetingID)
    } catch JobError.notFound {
      // Fall through to scan by job ID.
    }
  }

  let result = try await coordinator.list()
  if let job = result.jobs.first(where: {
    $0.jobID.uuidString.lowercased() == identifier.lowercased()
      || $0.jobID.uuidString == identifier
  }) {
    return job
  }

  throw MCPToolError.notFound("No job found for identifier: \(identifier)")
}

// MARK: - MCP tool errors

enum MCPToolError: Error, LocalizedError {
  case notFound(String)

  var errorDescription: String? {
    switch self {
    case .notFound(let message): message
    }
  }
}

// MARK: - AnyCodable

/// A type-erased Codable value for dynamic JSON-RPC tool arguments.
/// @unchecked Sendable because `Any` is not Sendable by default.
struct AnyCodable: Codable, @unchecked Sendable {
  let value: Any

  init(_ value: Any) {
    self.value = value
  }

  init(from decoder: Decoder) throws {
    let container = try decoder.singleValueContainer()
    if container.decodeNil() {
      value = NSNull()
    } else if let bool = try? container.decode(Bool.self) {
      value = bool
    } else if let int = try? container.decode(Int.self) {
      value = int
    } else if let double = try? container.decode(Double.self) {
      value = double
    } else if let string = try? container.decode(String.self) {
      value = string
    } else if let array = try? container.decode([AnyCodable].self) {
      value = array.map(\.value)
    } else if let dict = try? container.decode([String: AnyCodable].self) {
      value = dict.mapValues(\.value)
    } else {
      throw DecodingError.dataCorruptedError(
        in: container, debugDescription: "Unsupported AnyCodable type")
    }
  }

  func encode(to encoder: Encoder) throws {
    var container = encoder.singleValueContainer()
    switch value {
    case is NSNull:
      try container.encodeNil()
    case let bool as Bool:
      try container.encode(bool)
    case let int as Int:
      try container.encode(int)
    case let int32 as Int32:
      try container.encode(Int(int32))
    case let double as Double:
      try container.encode(double)
    case let string as String:
      try container.encode(string)
    case let array as [Any]:
      try container.encode(array.map { AnyCodable($0) })
    case let dict as [String: Any]:
      try container.encode(dict.mapValues { AnyCodable($0) })
    default:
      try container.encodeNil()
    }
  }

  /// Access the underlying value as a specific type.
  func asType<T>(_ type: T.Type) -> T? { value as? T }

  var asString: String? { value as? String }
  var asInt: Int? { value as? Int }
  var asBool: Bool? { value as? Bool }
  var asDouble: Double? { value as? Double }
  var asDict: [String: Any]? { value as? [String: Any] }
  var asArray: [Any]? { value as? [Any] }

  /// Convenience accessor for a nested dictionary key.
  func asDictValue(_ key: String) -> AnyCodable? {
    guard let dict = value as? [String: Any] else { return nil }
    return dict[key].map { AnyCodable($0) }
  }
}
