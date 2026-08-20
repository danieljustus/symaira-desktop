import Foundation
import XCTest

@testable import SymMeetMCP

final class AgentBridgeTests: XCTestCase {

  // MARK: - LocalAgentBridge

  func testLocalBridgeAlwaysReportsAgentUnavailable() async {
    let bridge = LocalAgentBridge()

    await assertAgentUnavailable { try await bridge.queryRecordingStatus() }
    await assertAgentUnavailable { try await bridge.requestRecording(purpose: "team standup") }
    await assertAgentUnavailable { try await bridge.stopRecording() }
  }

  func testAgentBridgeErrorDescriptions() {
    XCTAssertTrue(
      AgentBridgeError.agentUnavailable.errorDescription?.contains("SymMeetAgent") ?? false)
    XCTAssertTrue(
      AgentBridgeError.noActiveRecording.errorDescription?.contains("No active recording session")
        ?? false)
    XCTAssertTrue(
      AgentBridgeError.requestFailed("boom").errorDescription?.contains("boom") ?? false)
  }

  // MARK: - Agent contract Codable shapes

  func testAgentRecordingStatusCodableSnakeCaseRoundTrip() throws {
    let startedAt = Date(timeIntervalSince1970: 1_700_000_000)
    let status = AgentRecordingStatus(
      active: true, meetingID: "m-1", sessionID: "s-1", startedAt: startedAt, source: "microphone")

    let data = try JSONEncoder().encode(status)
    let json = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
    XCTAssertEqual(
      Set(json.keys), Set(["active", "meeting_id", "session_id", "started_at", "source"]))
    XCTAssertEqual(json["active"] as? Bool, true)
    XCTAssertEqual(json["meeting_id"] as? String, "m-1")

    let decoded = try JSONDecoder().decode(AgentRecordingStatus.self, from: data)
    XCTAssertEqual(decoded.active, true)
    XCTAssertEqual(decoded.meetingID, "m-1")
    XCTAssertEqual(decoded.sessionID, "s-1")
    XCTAssertEqual(decoded.startedAt, startedAt)
    XCTAssertEqual(decoded.source, "microphone")
  }

  func testAgentRecordingRequestResponseCodableSnakeCaseRoundTrip() throws {
    let response = AgentRecordingRequestResponse(
      status: "confirmation_required", meetingID: "m-1", sessionID: "s-1",
      message: "Request forwarded to the human agent.")

    let data = try JSONEncoder().encode(response)
    let json = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
    XCTAssertEqual(Set(json.keys), Set(["status", "meeting_id", "session_id", "message"]))
    XCTAssertEqual(json["status"] as? String, "confirmation_required")

    let decoded = try JSONDecoder().decode(AgentRecordingRequestResponse.self, from: data)
    XCTAssertEqual(decoded.status, "confirmation_required")
    XCTAssertEqual(decoded.meetingID, "m-1")
    XCTAssertEqual(decoded.message, "Request forwarded to the human agent.")
  }

  func testAgentRecordingStopResultCodableSnakeCaseRoundTrip() throws {
    let result = AgentRecordingStopResult(
      status: "stopped", meetingID: "m-1", segmentCount: 42, message: "Recording stopped.")

    let data = try JSONEncoder().encode(result)
    let json = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
    XCTAssertEqual(Set(json.keys), Set(["status", "meeting_id", "segment_count", "message"]))
    XCTAssertEqual(json["segment_count"] as? Int, 42)

    let decoded = try JSONDecoder().decode(AgentRecordingStopResult.self, from: data)
    XCTAssertEqual(decoded.status, "stopped")
    XCTAssertEqual(decoded.segmentCount, 42)
  }

  // MARK: - Recording handlers with a successful bridge

  func testRecordingStatusHandlerReturnsSnakeCaseJson() async throws {
    let bridge = RecordingBridgeMock()
    let handler = MeetingRecordingStatusHandler(agentBridge: bridge)

    let result = try await handler.execute(args: [:])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(
      Set(json.keys), Set(["active", "meeting_id", "session_id", "started_at", "source"]))
    XCTAssertEqual(json["active"] as? Bool, true)
    XCTAssertEqual(json["meeting_id"] as? String, "m-1")
  }

  func testRecordingRequestHandlerReturnsSnakeCaseJson() async throws {
    let bridge = RecordingBridgeMock()
    let handler = MeetingRecordingRequestHandler(agentBridge: bridge)

    let result = try await handler.execute(args: ["purpose": AnyCodable("team standup")])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(Set(json.keys), Set(["status", "meeting_id", "session_id", "message"]))
    XCTAssertEqual(json["status"] as? String, "confirmation_required")
    XCTAssertEqual(bridge.lastRequestedPurpose, "team standup")
  }

  func testRecordingStopHandlerReturnsSnakeCaseJson() async throws {
    let bridge = RecordingBridgeMock()
    let handler = MeetingRecordingStopHandler(agentBridge: bridge)

    let result = try await handler.execute(args: [:])

    XCTAssertFalse(result.isError)
    let json = try jsonObject(result)
    XCTAssertEqual(Set(json.keys), Set(["status", "meeting_id", "segment_count", "message"]))
    XCTAssertEqual(json["status"] as? String, "stopped")
    XCTAssertEqual(json["segment_count"] as? Int, 42)
  }

  // MARK: - Helpers

  private func assertAgentUnavailable(
    _ expression: () async throws -> Any,
    file: StaticString = #filePath,
    line: UInt = #line
  ) async {
    do {
      _ = try await expression()
      XCTFail("Expected AgentBridgeError.agentUnavailable", file: file, line: line)
    } catch let error as AgentBridgeError {
      guard case .agentUnavailable = error else {
        XCTFail("Unexpected AgentBridgeError: \(error)", file: file, line: line)
        return
      }
    } catch {
      XCTFail("Unexpected error type: \(error)", file: file, line: line)
    }
  }
}

/// A minimal agent-bridge double returning success values, used to assert the
/// JSON shapes the recording handlers emit on the success path.
private final class RecordingBridgeMock: AgentBridge, @unchecked Sendable {
  var lastRequestedPurpose: String?

  func queryRecordingStatus() async throws -> AgentRecordingStatus {
    AgentRecordingStatus(
      active: true, meetingID: "m-1", sessionID: "s-1",
      startedAt: Date(timeIntervalSince1970: 1_700_000_000), source: "microphone")
  }

  func requestRecording(purpose: String) async throws -> AgentRecordingRequestResponse {
    lastRequestedPurpose = purpose
    return AgentRecordingRequestResponse(
      status: "confirmation_required", meetingID: "m-1", sessionID: "s-1",
      message: "Request forwarded to the human agent.")
  }

  func stopRecording() async throws -> AgentRecordingStopResult {
    AgentRecordingStopResult(
      status: "stopped", meetingID: "m-1", segmentCount: 42, message: "Recording stopped.")
  }
}
