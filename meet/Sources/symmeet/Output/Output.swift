import Foundation

enum Output {
  static func writeJSON<T: Encodable>(_ value: T) throws {
    let encoder = JSONEncoder()
    encoder.keyEncodingStrategy = .convertToSnakeCase
    encoder.outputFormatting = [.sortedKeys]
    var data = try encoder.encode(value)
    data.append(0x0A)
    FileHandle.standardOutput.write(data)
  }

  static func writeLine(_ value: String) {
    FileHandle.standardOutput.write(Data((value + "\n").utf8))
  }

  static func writeRaw(_ value: String) {
    FileHandle.standardOutput.write(Data(value.utf8))
  }

  static func writeError(_ value: String) {
    FileHandle.standardError.write(Data((value + "\n").utf8))
  }
}

/// Machine-readable error envelope emitted on stdout when a --json
/// invocation fails.  Stable codes come from CLIError.code.
struct ErrorEnvelope: Encodable {
  let error: ErrorPayload

  struct ErrorPayload: Encodable {
    let code: String
    let message: String
  }
}
