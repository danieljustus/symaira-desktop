import Foundation
import XCTest

@testable import SymMeetCore

final class PrivacyPolicyTests: XCTestCase {
  func testFreshInteractiveRecordStartsOnlyItsOwnSession() async throws {
    let now = Date(timeIntervalSince1970: 1_000)
    let scope = RecordingScope(meetingID: UUID(), purpose: "operator_requested_recording")
    let authorizer = FixedAuthorizer(
      decision: InteractiveAuthorizationDecision(
        operatorAttested: true,
        noticeAt: now.addingTimeInterval(-1),
        scope: scope,
        expiresAt: now.addingTimeInterval(60)
      )
    )
    let authorization = RecordingAuthorization(authorizer: authorizer, now: { now })
    let sessionID = UUID()
    let record = try await authorization.requestAuthorization(sessionID: sessionID, scope: scope)

    try await authorization.startRecording(sessionID: sessionID, authorization: record)
    await assertPrivacyError(
      try await authorization.startRecording(sessionID: sessionID, authorization: record),
      equals: .authorizationAlreadyUsed
    )

    try await authorization.stopRecording(sessionID: sessionID)
    await assertPrivacyError(
      try await authorization.startRecording(sessionID: sessionID, authorization: record),
      equals: .authorizationAlreadyUsed
    )
  }

  func testExpiredWrongSessionAndRestartedAuthorizationFailClosed() async throws {
    let now = Date(timeIntervalSince1970: 1_000)
    let scope = RecordingScope(meetingID: UUID(), purpose: "operator_requested_recording")
    let authorizer = FixedAuthorizer(
      decision: InteractiveAuthorizationDecision(
        operatorAttested: true,
        noticeAt: now.addingTimeInterval(-1),
        scope: scope,
        expiresAt: now.addingTimeInterval(30)
      )
    )
    let authorization = RecordingAuthorization(authorizer: authorizer, now: { now })
    let sessionID = UUID()
    let record = try await authorization.requestAuthorization(sessionID: sessionID, scope: scope)

    await assertPrivacyError(
      try await authorization.startRecording(sessionID: UUID(), authorization: record),
      equals: .authorizationNotForSession
    )
    await authorization.invalidateForProcessRestart()
    await assertPrivacyError(
      try await authorization.startRecording(sessionID: sessionID, authorization: record),
      equals: .invalidAuthorizationRecord
    )

    let expiredAuthorizer = FixedAuthorizer(
      decision: InteractiveAuthorizationDecision(
        operatorAttested: true,
        noticeAt: now.addingTimeInterval(-1),
        scope: scope,
        expiresAt: now.addingTimeInterval(1)
      )
    )
    let expired = RecordingAuthorization(
      authorizer: expiredAuthorizer, now: { now.addingTimeInterval(2) })
    await assertPrivacyError(
      try await expired.requestAuthorization(sessionID: UUID(), scope: scope),
      equals: .invalidInteractiveAttestation
    )
  }

  func testInvalidInteractiveAttestationAndRemoteProcessingAreRejected() async throws {
    let now = Date(timeIntervalSince1970: 1_000)
    let scope = RecordingScope(meetingID: UUID(), purpose: "operator_requested_recording")
    let authorizer = FixedAuthorizer(
      decision: InteractiveAuthorizationDecision(
        operatorAttested: false,
        noticeAt: now,
        scope: scope,
        expiresAt: now.addingTimeInterval(60)
      )
    )
    let authorization = RecordingAuthorization(authorizer: authorizer, now: { now })

    await assertPrivacyError(
      try await authorization.requestAuthorization(sessionID: UUID(), scope: scope),
      equals: .invalidInteractiveAttestation
    )
    XCTAssertNoThrow(try LocalProcessingPolicy().validate(.local))
    XCTAssertThrowsError(try LocalProcessingPolicy().validate(.remote)) { error in
      XCTAssertEqual(error as? PrivacyError, .localProcessingOnly)
    }
  }

  func testRecordingStartRejectsARecordNotMintedByTheInteractiveAuthorizer() async throws {
    let now = Date(timeIntervalSince1970: 1_000)
    let scope = RecordingScope(meetingID: UUID(), purpose: "operator_requested_recording")
    let sessionID = UUID()
    let forged = ConsentRecord(
      sessionID: sessionID,
      operatorAttested: true,
      noticeAt: now,
      scope: scope,
      expiresAt: now.addingTimeInterval(60)
    )
    let authorization = RecordingAuthorization(
      authorizer: FixedAuthorizer(
        decision: InteractiveAuthorizationDecision(
          operatorAttested: true,
          noticeAt: now,
          scope: scope,
          expiresAt: now.addingTimeInterval(60)
        )
      ),
      now: { now }
    )

    await assertPrivacyError(
      try await authorization.startRecording(sessionID: sessionID, authorization: forged),
      equals: .invalidAuthorizationRecord
    )
  }

  func testStructuredLoggerRedactsSensitiveMetadata() throws {
    let event = RedactedStructuredLogger.event(
      name: "retention_cleanup",
      status: "failed",
      meetingID: UUID(),
      jobID: UUID(),
      metadata: [
        "retry_count": "2",
        "transcript": "private words",
        "person_name": "Ada",
        "calendar_title": "Sensitive meeting",
        "provider_key": "provider-secret",
        "path": "/Users/example/meeting",
      ]
    )
    let serialized = try RedactedStructuredLogger.encode(event)

    XCTAssertTrue(serialized.contains("retention_cleanup"))
    XCTAssertTrue(serialized.contains("retry_count"))
    for sensitiveValue in [
      "private words", "Ada", "Sensitive meeting", "provider-secret", "/Users/example",
    ] {
      XCTAssertFalse(serialized.contains(sensitiveValue))
    }
  }
}

private struct FixedAuthorizer: InteractiveRecordingAuthorizer {
  let decision: InteractiveAuthorizationDecision

  func requestAuthorization(
    for _: RecordingAuthorizationRequest
  ) async throws -> InteractiveAuthorizationDecision {
    decision
  }
}

private func assertPrivacyError<T>(
  _ expression: @autoclosure () async throws -> T,
  equals expected: PrivacyError
) async {
  do {
    _ = try await expression()
    XCTFail("Expected a privacy error")
  } catch {
    XCTAssertEqual(error as? PrivacyError, expected)
  }
}
