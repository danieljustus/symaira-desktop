import Foundation

public struct RecordingScope: Codable, Equatable, Sendable {
  public let meetingID: UUID
  public let purpose: String

  public init(meetingID: UUID, purpose: String) {
    self.meetingID = meetingID
    self.purpose = purpose
  }
}

public struct RecordingAuthorizationRequest: Equatable, Sendable {
  public let sessionID: UUID
  public let scope: RecordingScope
  public let requestedAt: Date

  public init(sessionID: UUID, scope: RecordingScope, requestedAt: Date) {
    self.sessionID = sessionID
    self.scope = scope
    self.requestedAt = requestedAt
  }
}

/// The data returned by a UI or other interactive consent implementation.
public struct InteractiveAuthorizationDecision: Equatable, Sendable {
  public let operatorAttested: Bool
  public let noticeAt: Date
  public let scope: RecordingScope
  public let expiresAt: Date

  public init(operatorAttested: Bool, noticeAt: Date, scope: RecordingScope, expiresAt: Date) {
    self.operatorAttested = operatorAttested
    self.noticeAt = noticeAt
    self.scope = scope
    self.expiresAt = expiresAt
  }
}

/// The only source of a record that can start a recording session.
public protocol InteractiveRecordingAuthorizer: Sendable {
  func requestAuthorization(
    for request: RecordingAuthorizationRequest
  ) async throws -> InteractiveAuthorizationDecision
}

/// A short-lived, single-session record. Its initializer is intentionally not
/// public: only `RecordingAuthorization` may mint one after interactive input.
public struct ConsentRecord: Codable, Equatable, Sendable {
  public let recordID: UUID
  public let sessionID: UUID
  public let operatorAttested: Bool
  public let noticeAt: Date
  public let scope: RecordingScope
  public let expiresAt: Date

  init(
    recordID: UUID = UUID(),
    sessionID: UUID,
    operatorAttested: Bool,
    noticeAt: Date,
    scope: RecordingScope,
    expiresAt: Date
  ) {
    self.recordID = recordID
    self.sessionID = sessionID
    self.operatorAttested = operatorAttested
    self.noticeAt = noticeAt
    self.scope = scope
    self.expiresAt = expiresAt
  }
}
