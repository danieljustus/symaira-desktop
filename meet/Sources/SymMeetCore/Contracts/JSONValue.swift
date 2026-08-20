import Foundation

/// A lossless JSON value used to preserve unknown additive contract fields.
public indirect enum JSONValue: Codable, Equatable, Sendable {
  case array([JSONValue])
  case bool(Bool)
  case number(Double)
  case object([String: JSONValue])
  case string(String)
  case null

  public init(from decoder: Decoder) throws {
    let container = try decoder.singleValueContainer()

    if container.decodeNil() {
      self = .null
    } else if let value = try? container.decode(Bool.self) {
      self = .bool(value)
    } else if let value = try? container.decode(Double.self) {
      self = .number(value)
    } else if let value = try? container.decode(String.self) {
      self = .string(value)
    } else if let value = try? container.decode([JSONValue].self) {
      self = .array(value)
    } else {
      self = .object(try container.decode([String: JSONValue].self))
    }
  }

  public func encode(to encoder: Encoder) throws {
    var container = encoder.singleValueContainer()

    switch self {
    case .array(let value): try container.encode(value)
    case .bool(let value): try container.encode(value)
    case .number(let value): try container.encode(value)
    case .object(let value): try container.encode(value)
    case .string(let value): try container.encode(value)
    case .null: try container.encodeNil()
    }
  }
}

struct AnyCodingKey: CodingKey, Hashable {
  let stringValue: String
  let intValue: Int?

  init?(stringValue: String) {
    self.stringValue = stringValue
    intValue = nil
  }

  init?(intValue: Int) {
    stringValue = String(intValue)
    self.intValue = intValue
  }

  init(_ stringValue: String) {
    self.stringValue = stringValue
    intValue = nil
  }
}
