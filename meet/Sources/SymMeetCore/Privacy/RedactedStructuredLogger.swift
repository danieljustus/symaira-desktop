import Foundation

public struct PrivacyLogEvent: Codable, Equatable, Sendable {
  public let event: String
  public let status: String
  public let meetingID: UUID?
  public let jobID: UUID?
  public let metadata: [String: String]
}

/// Produces structured records that retain identifiers and state while
/// removing content and identity-bearing fields before anything can be logged.
public enum RedactedStructuredLogger {
  public static func event(
    name: String,
    status: String,
    meetingID: UUID? = nil,
    jobID: UUID? = nil,
    metadata: [String: String] = [:]
  ) -> PrivacyLogEvent {
    PrivacyLogEvent(
      event: name,
      status: status,
      meetingID: meetingID,
      jobID: jobID,
      metadata: sanitized(metadata)
    )
  }

  public static func encode(_ event: PrivacyLogEvent) throws -> String {
    let encoder = JSONEncoder()
    encoder.keyEncodingStrategy = .convertToSnakeCase
    encoder.outputFormatting = [.sortedKeys]
    return String(decoding: try encoder.encode(event), as: UTF8.self)
  }

  private static func sanitized(_ metadata: [String: String]) -> [String: String] {
    let allowed = Set(["policy", "retry_count", "result"])
    return metadata.reduce(into: [:]) { output, item in
      guard allowed.contains(item.key), !item.value.contains("/") else { return }
      output[item.key] = item.value
    }
  }
}
