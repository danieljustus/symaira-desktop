import Foundation

/// Enforces fresh, interactive consent for each recording session.
public actor RecordingAuthorization {
  private enum SessionState: Sendable {
    case authorized(UUID)
    case active(UUID)
    case closed
  }

  private let authorizer: any InteractiveRecordingAuthorizer
  private let now: @Sendable () -> Date
  private var records: [UUID: ConsentRecord] = [:]
  private var sessions: [UUID: SessionState] = [:]

  public init(
    authorizer: any InteractiveRecordingAuthorizer,
    now: @escaping @Sendable () -> Date = Date.init
  ) {
    self.authorizer = authorizer
    self.now = now
  }

  public func requestAuthorization(
    sessionID: UUID,
    scope: RecordingScope
  ) async throws -> ConsentRecord {
    let requestedAt = now()
    let decision = try await authorizer.requestAuthorization(
      for: RecordingAuthorizationRequest(
        sessionID: sessionID,
        scope: scope,
        requestedAt: requestedAt
      )
    )

    guard
      decision.operatorAttested,
      decision.scope == scope,
      decision.noticeAt <= requestedAt,
      decision.expiresAt > requestedAt
    else {
      throw PrivacyError.invalidInteractiveAttestation
    }

    let record = ConsentRecord(
      sessionID: sessionID,
      operatorAttested: decision.operatorAttested,
      noticeAt: decision.noticeAt,
      scope: decision.scope,
      expiresAt: decision.expiresAt
    )
    records[record.recordID] = record
    sessions[sessionID] = .authorized(record.recordID)
    return record
  }

  public func startRecording(sessionID: UUID, authorization: ConsentRecord) throws {
    guard let mintedRecord = records[authorization.recordID], mintedRecord == authorization else {
      throw PrivacyError.invalidAuthorizationRecord
    }
    guard authorization.sessionID == sessionID else {
      throw PrivacyError.authorizationNotForSession
    }
    guard authorization.expiresAt > now() else {
      throw PrivacyError.authorizationExpired
    }
    guard case .authorized(authorization.recordID) = sessions[sessionID] else {
      throw PrivacyError.authorizationAlreadyUsed
    }

    sessions[sessionID] = .active(authorization.recordID)
  }

  public func stopRecording(sessionID: UUID) throws {
    guard case .active = sessions[sessionID] else {
      throw PrivacyError.recordingNotActive
    }
    sessions[sessionID] = .closed
  }

  /// Process restart is fail-closed: no in-memory authorization survives.
  public func invalidateForProcessRestart() {
    records.removeAll()
    sessions.removeAll()
  }
}

public enum ProcessingLocation: Sendable {
  case local
  case remote
}

public struct LocalProcessingPolicy: Sendable {
  public init() {}

  public func validate(_ location: ProcessingLocation) throws {
    guard location == .local else { throw PrivacyError.localProcessingOnly }
  }
}
