import Foundation
import SymMeetCore

// MARK: - Meeting job status handler

struct MeetingJobStatusHandler: MCPToolHandler {
  let toolName = "meeting_job_status"
  let store: MeetingStore
  let dataRoot: URL

  func execute(args: [String: AnyCodable]) async throws -> MCPToolResult {
    guard let jobID = args["job_id"]?.asString else {
      return .error("Missing required parameter: job_id")
    }

    let coordinator = JobCoordinator(dataRoot: dataRoot)
    let job = try await resolveJob(coordinator: coordinator, identifier: jobID)

    let output = JobStatusOutput(
      jobID: job.jobID.uuidString.lowercased(),
      meetingID: job.meetingID.uuidString.lowercased(),
      status: job.status.rawValue,
      attempt: job.attempt,
      engine: job.engine.map {
        EngineOutput(engineID: $0.engineID, modelID: $0.modelID)
      }
    )

    return try MCPToolResult.json(output)
  }
}

// MARK: - Meeting job cancel handler

struct MeetingJobCancelHandler: MCPToolHandler {
  let toolName = "meeting_job_cancel"
  let store: MeetingStore
  let dataRoot: URL

  func execute(args: [String: AnyCodable]) async throws -> MCPToolResult {
    guard let jobID = args["job_id"]?.asString else {
      return .error("Missing required parameter: job_id")
    }

    let coordinator = JobCoordinator(dataRoot: dataRoot)
    let job = try await resolveJob(coordinator: coordinator, identifier: jobID)

    guard job.status == .queued || job.status.isActive else {
      return .text(
        "{\"status\":\"\(job.status.rawValue)\",\"message\":\"Job is already in terminal state.\"}")
    }

    let handle = try await coordinator.lock.acquire(meetingID: job.meetingID)
    if job.status == .queued {
      _ = try await coordinator.advance(
        meetingID: job.meetingID, to: .cancelled, using: handle,
        note: "cancelled via MCP")
    } else {
      _ = try await coordinator.requestCancellation(meetingID: job.meetingID, using: handle)
      _ = try await coordinator.confirmCancelled(meetingID: job.meetingID, using: handle)
    }

    return .text(
      "{\"meeting_id\":\"\(job.meetingID.uuidString.lowercased())\",\"status\":\"cancelled\",\"message\":\"Job cancelled.\"}"
    )
  }
}

// MARK: - Output types

private struct JobStatusOutput: Encodable {
  let jobID: String
  let meetingID: String
  let status: String
  let attempt: Int
  let engine: EngineOutput?

  private enum CodingKeys: String, CodingKey {
    case jobID = "job_id"
    case meetingID = "meeting_id"
    case status, attempt, engine
  }
}

private struct EngineOutput: Encodable {
  let engineID: String
  let modelID: String

  private enum CodingKeys: String, CodingKey {
    case engineID = "engine_id"
    case modelID = "model_id"
  }
}
