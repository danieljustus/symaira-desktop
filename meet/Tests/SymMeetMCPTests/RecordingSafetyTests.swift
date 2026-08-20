import XCTest

@testable import SymMeetMCP

final class RecordingSafetyTests: XCTestCase {

  // MARK: - Recording request safety

  /// A headless client cannot start recording even if it supplies invented consent fields.
  func testRecordingRequestRejectsConsentFields() async {
    let bridge = MockAgentBridge()
    let handler = MeetingRecordingRequestHandler(agentBridge: bridge)

    // Attempt with an invented consent field
    let result = await (try? handler.execute(args: [
      "purpose": AnyCodable("test meeting"),
      "consent": AnyCodable(true),
    ]))

    XCTAssertNotNil(result)
    XCTAssertTrue(result?.isError ?? false)
    XCTAssertTrue(
      result?.content.first?.text?.contains("Consent fields are not accepted") ?? false,
      "Must reject invented consent fields")

    // Verify the agent bridge was NOT called
    XCTAssertEqual(bridge.requestRecordingCallCount, 0)
  }

  func testRecordingRequestRejectsConsentedField() async {
    let bridge = MockAgentBridge()
    let handler = MeetingRecordingRequestHandler(agentBridge: bridge)

    let result = await (try? handler.execute(args: [
      "purpose": AnyCodable("test meeting"),
      "consented": AnyCodable(true),
    ]))

    XCTAssertNotNil(result)
    XCTAssertTrue(result?.isError ?? false)
    XCTAssertEqual(bridge.requestRecordingCallCount, 0)
  }

  func testRecordingRequestRejectsAuthorizationToken() async {
    let bridge = MockAgentBridge()
    let handler = MeetingRecordingRequestHandler(agentBridge: bridge)

    let result = await (try? handler.execute(args: [
      "purpose": AnyCodable("test meeting"),
      "authorization_token": AnyCodable("fake-token-123"),
    ]))

    XCTAssertNotNil(result)
    XCTAssertTrue(result?.isError ?? false)
    XCTAssertEqual(bridge.requestRecordingCallCount, 0)
  }

  /// Recording request requires purpose parameter.
  func testRecordingRequestRequiresPurpose() async {
    let bridge = MockAgentBridge()
    let handler = MeetingRecordingRequestHandler(agentBridge: bridge)

    let result = await (try? handler.execute(args: [:]))

    XCTAssertNotNil(result)
    XCTAssertTrue(result?.isError ?? false)
    XCTAssertTrue(
      result?.content.first?.text?.contains("Missing required parameter: purpose") ?? false)
  }

  /// Recording request delegates to the agent bridge (no direct capture fallback).
  func testRecordingRequestDelegatesToAgent() async {
    let bridge = MockAgentBridge()
    let handler = MeetingRecordingRequestHandler(agentBridge: bridge)

    let result = await (try? handler.execute(args: [
      "purpose": AnyCodable("team standup")
    ]))

    XCTAssertNotNil(result)
    XCTAssertEqual(bridge.requestRecordingCallCount, 1)
    XCTAssertEqual(bridge.lastRequestedPurpose, "team standup")
  }

  /// When agent is unavailable, recording request fails closed.
  func testRecordingRequestFailsClosedWhenAgentUnavailable() async {
    let bridge = MockAgentBridge()
    bridge.requestRecordingResult = .failure(AgentBridgeError.agentUnavailable)
    let handler = MeetingRecordingRequestHandler(agentBridge: bridge)

    let result = await (try? handler.execute(args: [
      "purpose": AnyCodable("team standup")
    ]))

    XCTAssertNotNil(result)
    XCTAssertTrue(result?.isError ?? false)
    XCTAssertTrue(
      result?.content.first?.text?.contains("Recording agent is unavailable") ?? false,
      "Must fail closed, never fall back to direct CLI capture")
  }

  // MARK: - Recording stop safety

  /// Stopping may proceed without additional confirmation (it reduces exposure).
  func testRecordingStopDelegatesToAgent() async {
    let bridge = MockAgentBridge()
    let handler = MeetingRecordingStopHandler(agentBridge: bridge)

    let result = await (try? handler.execute(args: [:]))

    XCTAssertNotNil(result)
    XCTAssertEqual(bridge.stopRecordingCallCount, 1)
  }

  /// When agent is unavailable for stop, report actionable error.
  func testRecordingStopFailsWithActionableError() async {
    let bridge = MockAgentBridge()
    bridge.stopRecordingResult = .failure(AgentBridgeError.agentUnavailable)
    let handler = MeetingRecordingStopHandler(agentBridge: bridge)

    let result = await (try? handler.execute(args: [:]))

    XCTAssertNotNil(result)
    XCTAssertTrue(result?.isError ?? false)
    XCTAssertTrue(
      result?.content.first?.text?.contains("No recording agent") ?? false)
  }

  // MARK: - Recording status safety

  /// Status query fails with actionable error when agent is unavailable.
  func testRecordingStatusFailsWithActionableError() async {
    let bridge = MockAgentBridge()
    bridge.queryRecordingStatusResult = .failure(AgentBridgeError.agentUnavailable)
    let handler = MeetingRecordingStatusHandler(agentBridge: bridge)

    let result = await (try? handler.execute(args: [:]))

    XCTAssertNotNil(result)
    XCTAssertTrue(result?.isError ?? false)
    XCTAssertTrue(
      result?.content.first?.text?.contains("No recording agent") ?? false)
  }

  // MARK: - Agent bridge never accepts consent

  /// The agent bridge protocol does not expose any method that accepts a consent Boolean.
  func testAgentBridgeProtocolHasNoConsentParameter() {
    // This is a compile-time safety check: if the protocol gained a method
    // accepting a consent parameter, this test file would fail to compile
    // because the mock doesn't implement it. This is intentional.
    //
    // The safety invariant is enforced by the handler layer:
    // MeetingRecordingRequestHandler.execute() checks for consent-related
    // keys and rejects them before reaching the agent bridge.
    let bridge = MockAgentBridge()
    XCTAssertNotNil(bridge)
  }
}

// MARK: - Mock Agent Bridge

/// A test double for AgentBridge that records calls and returns configurable results.
private final class MockAgentBridge: AgentBridge, @unchecked Sendable {
  var queryRecordingStatusResult: Result<AgentRecordingStatus, Error> = .success(
    AgentRecordingStatus(
      active: false, meetingID: nil, sessionID: nil, startedAt: nil, source: nil))

  var requestRecordingResult: Result<AgentRecordingRequestResponse, Error> = .success(
    AgentRecordingRequestResponse(
      status: "confirmation_required", meetingID: nil, sessionID: nil,
      message: "Request forwarded to the human agent."))

  var stopRecordingResult: Result<AgentRecordingStopResult, Error> = .success(
    AgentRecordingStopResult(
      status: "stopped", meetingID: nil, segmentCount: nil,
      message: "Recording stopped."))

  var queryRecordingStatusCallCount = 0
  var requestRecordingCallCount = 0
  var stopRecordingCallCount = 0
  var lastRequestedPurpose: String?

  func queryRecordingStatus() async throws -> AgentRecordingStatus {
    queryRecordingStatusCallCount += 1
    return try queryRecordingStatusResult.get()
  }

  func requestRecording(purpose: String) async throws -> AgentRecordingRequestResponse {
    requestRecordingCallCount += 1
    lastRequestedPurpose = purpose
    return try requestRecordingResult.get()
  }

  func stopRecording() async throws -> AgentRecordingStopResult {
    stopRecordingCallCount += 1
    return try stopRecordingResult.get()
  }
}
