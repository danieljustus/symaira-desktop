import Foundation

// MARK: - Meeting recording status handler

struct MeetingRecordingStatusHandler: MCPToolHandler {
  let toolName = "meeting_recording_status"
  let agentBridge: AgentBridge

  func execute(args: [String: AnyCodable]) async throws -> MCPToolResult {
    do {
      let status = try await agentBridge.queryRecordingStatus()
      return try MCPToolResult.json(status)
    } catch AgentBridgeError.agentUnavailable {
      return .error(
        "No recording agent is available. Recordings must be managed through the SymMeetAgent app.")
    }
  }
}

// MARK: - Meeting recording request handler

struct MeetingRecordingRequestHandler: MCPToolHandler {
  let toolName = "meeting_recording_request"
  let agentBridge: AgentBridge

  func execute(args: [String: AnyCodable]) async throws -> MCPToolResult {
    guard let purpose = args["purpose"]?.asString, !purpose.isEmpty else {
      return .error("Missing required parameter: purpose")
    }

    let hasConsentField = args["consent"] != nil || args["consented"] != nil
      || args["authorization_token"] != nil
    if hasConsentField {
      return .error(
        "Consent fields are not accepted. Recording authorization requires interactive human confirmation through the SymMeetAgent."
      )
    }

    do {
      let request = try await agentBridge.requestRecording(purpose: purpose)
      return try MCPToolResult.json(request)
    } catch AgentBridgeError.agentUnavailable {
      return .error(
        "Recording agent is unavailable. Cannot start recording without an authorized agent. Do not fall back to direct CLI capture.")
    }
  }
}

// MARK: - Meeting recording stop handler

struct MeetingRecordingStopHandler: MCPToolHandler {
  let toolName = "meeting_recording_stop"
  let agentBridge: AgentBridge

  func execute(args: [String: AnyCodable]) async throws -> MCPToolResult {
    do {
      let result = try await agentBridge.stopRecording()
      return try MCPToolResult.json(result)
    } catch AgentBridgeError.agentUnavailable {
      return .error("No recording agent is available to stop.")
    } catch AgentBridgeError.noActiveRecording {
      return .error("No active recording to stop.")
    }
  }
}
