import Foundation
import SymMeetCore

// MARK: - Agent Bridge Errors

public enum AgentBridgeError: Error, LocalizedError {
  case agentUnavailable
  case noActiveRecording
  case requestFailed(String)

  public var errorDescription: String? {
    switch self {
    case .agentUnavailable:
      "The recording agent is not available. Recordings must be managed through the SymMeetAgent app."
    case .noActiveRecording:
      "No active recording session to stop."
    case .requestFailed(let message):
      "Agent request failed: \(message)"
    }
  }
}

// MARK: - Recording status

public struct AgentRecordingStatus: Codable, Sendable {
  let active: Bool
  let meetingID: String?
  let sessionID: String?
  let startedAt: Date?
  let source: String?

  private enum CodingKeys: String, CodingKey {
    case active
    case meetingID = "meeting_id"
    case sessionID = "session_id"
    case startedAt = "started_at"
    case source
  }
}

public struct AgentRecordingRequestResponse: Codable, Sendable {
  let status: String
  let meetingID: String?
  let sessionID: String?
  let message: String

  private enum CodingKeys: String, CodingKey {
    case status
    case meetingID = "meeting_id"
    case sessionID = "session_id"
    case message
  }
}

public struct AgentRecordingStopResult: Codable, Sendable {
  let status: String
  let meetingID: String?
  let segmentCount: Int?
  let message: String

  private enum CodingKeys: String, CodingKey {
    case status
    case meetingID = "meeting_id"
    case segmentCount = "segment_count"
    case message
  }
}

// MARK: - Agent Bridge Protocol

/// The bridge between the MCP server and the signed SymMeetAgent for
/// recording authorization and control. The agent is the only entity
/// that may authorize recording; the MCP server never bypasses this.
public protocol AgentBridge: Sendable {
  /// Queries the current recording status from the agent.
  func queryRecordingStatus() async throws -> AgentRecordingStatus

  /// Sends a recording request to the agent. Returns confirmation_required
  /// because only the human-facing agent can authorize capture.
  func requestRecording(purpose: String) async throws -> AgentRecordingRequestResponse

  /// Stops an active recording.
  func stopRecording() async throws -> AgentRecordingStopResult
}

// MARK: - Local Agent Bridge

/// A local agent bridge that communicates with SymMeetAgent via IPC.
///
/// In the current implementation, this is a stub that always returns
/// agent-unavailable. The real implementation will authenticate the peer
/// using process identity plus a short-lived challenge, not a secret
/// stored in config.
public struct LocalAgentBridge: AgentBridge {
  public init() {}

  public func queryRecordingStatus() async throws -> AgentRecordingStatus {
    throw AgentBridgeError.agentUnavailable
  }

  public func requestRecording(purpose: String) async throws -> AgentRecordingRequestResponse {
    throw AgentBridgeError.agentUnavailable
  }

  public func stopRecording() async throws -> AgentRecordingStopResult {
    throw AgentBridgeError.agentUnavailable
  }
}
