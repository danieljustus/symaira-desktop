import Foundation

public enum StoreError: Error, Equatable, LocalizedError, Sendable {
  case alreadyExists
  case invalidMeetingID
  case invalidRelativePath
  case malformedArtifact
  case missing
  case operationFailed
  case unsafePath

  public var errorDescription: String? {
    switch self {
    case .alreadyExists: "A meeting artifact with this identifier already exists."
    case .invalidMeetingID: "The meeting identifier is invalid."
    case .invalidRelativePath: "The artifact path is not a safe relative path."
    case .malformedArtifact: "The meeting artifact is malformed."
    case .missing: "The meeting artifact does not exist."
    case .operationFailed: "The artifact-store operation could not be completed."
    case .unsafePath: "The artifact path is outside the configured data root."
    }
  }
}

public enum StoreDiagnosticCode: String, Codable, Equatable, Sendable {
  case invalidMeetingDirectory = "invalid_meeting_directory"
  case malformedManifest = "malformed_manifest"
  case unsafePath = "unsafe_path"
}

public struct StoreDiagnostic: Codable, Equatable, Sendable {
  public let meetingID: String
  public let code: StoreDiagnosticCode

  public init(meetingID: String, code: StoreDiagnosticCode) {
    self.meetingID = meetingID
    self.code = code
  }
}

public struct MeetingList: Equatable, Sendable {
  public let meetings: [MeetingManifest]
  public let diagnostics: [StoreDiagnostic]

  public init(meetings: [MeetingManifest], diagnostics: [StoreDiagnostic]) {
    self.meetings = meetings
    self.diagnostics = diagnostics
  }
}
