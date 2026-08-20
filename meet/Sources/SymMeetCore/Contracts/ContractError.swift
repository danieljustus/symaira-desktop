import Foundation

public enum ContractError: Error, Equatable, LocalizedError, Sendable {
  case invalidIdentifier(String)
  case invalidStateTransition(from: MeetingLifecycleState, to: MeetingLifecycleState)
  case invalidTimeRange(startMS: Int, endMS: Int)
  case unsupportedSchemaVersion(Int)

  public var errorDescription: String? {
    switch self {
    case .invalidIdentifier(let field):
      "Invalid contract identifier for \(field)."
    case .invalidStateTransition(let from, let to):
      "Invalid meeting lifecycle transition from \(from.rawValue) to \(to.rawValue)."
    case .invalidTimeRange:
      "A media segment must have a non-negative start and a later end."
    case .unsupportedSchemaVersion(let version):
      "Unsupported contract schema version \(version)."
    }
  }
}

public enum ContractCodec {
  public static func decoder() -> JSONDecoder {
    let decoder = JSONDecoder()
    decoder.dateDecodingStrategy = .iso8601
    return decoder
  }

  public static func encoder(prettyPrinted: Bool = false) -> JSONEncoder {
    let encoder = JSONEncoder()
    encoder.dateEncodingStrategy = .iso8601
    encoder.outputFormatting = prettyPrinted ? [.prettyPrinted, .sortedKeys] : [.sortedKeys]
    return encoder
  }
}
