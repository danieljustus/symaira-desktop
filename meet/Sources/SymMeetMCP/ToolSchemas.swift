import Foundation
import SymMeetCore

// MARK: - MCP Tool Schema

/// A single MCP tool definition exposed to agents.
struct MCPToolSchema: Codable, Sendable {
  let name: String
  let description: String
  let inputSchema: MCPInputSchema

  private enum CodingKeys: String, CodingKey {
    case name
    case description
    case inputSchema = "input_schema"
  }
}

/// The JSON Schema describing a tool's input parameters.
struct MCPInputSchema: Codable, Sendable {
  let type: String
  let properties: [String: MCPPropertySchema]
  let required: [String]
}

/// A single JSON Schema property definition.
struct MCPPropertySchema: Codable, Sendable {
  let type: String
  let description: String
  let enumValues: [String]?
  let defaultValue: AnyCodable?

  private enum CodingKeys: String, CodingKey {
    case type, description
    case defaultValue = "default"
    case enumValues = "enum"
  }
}

// MARK: - Tool registry

enum MCPToolRegistry {
  /// All tools exposed by the MCP server, in protocol order.
  static let tools: [MCPToolSchema] = [
    meetingList,
    meetingGet,
    meetingTranscribe,
    meetingJobStatus,
    meetingJobCancel,
    meetingExport,
    meetingRecordingStatus,
    meetingRecordingRequest,
    meetingRecordingStop,
  ]

  // MARK: - Individual tool schemas

  static let meetingList = MCPToolSchema(
    name: "meeting_list",
    description:
      "List portable meeting artifacts. Returns meeting IDs, sources, and creation timestamps.",
    inputSchema: MCPInputSchema(
      type: "object",
      properties: [:],
      required: []
    )
  )

  static let meetingGet = MCPToolSchema(
    name: "meeting_get",
    description:
      "Get a single meeting artifact. Transcript segments require an explicit request and bounded limit.",
    inputSchema: MCPInputSchema(
      type: "object",
      properties: [
        "meeting_id": MCPPropertySchema(
          type: "string",
          description: "The meeting UUID.",
          enumValues: nil, defaultValue: nil),
        "include_segments": MCPPropertySchema(
          type: "boolean",
          description: "Include transcript segments (default: false).",
          enumValues: nil, defaultValue: AnyCodable(false)),
        "segment_limit": MCPPropertySchema(
          type: "integer",
          description: "Maximum segments to return when include_segments is true (max 500).",
          enumValues: nil, defaultValue: AnyCodable(50)),
      ],
      required: ["meeting_id"]
    )
  )

  static let meetingTranscribe = MCPToolSchema(
    name: "meeting_transcribe",
    description:
      "Transcribe a local audio or video file into a meeting artifact. Requires an installed model.",
    inputSchema: MCPInputSchema(
      type: "object",
      properties: [
        "file": MCPPropertySchema(
          type: "string",
          description: "Path to the audio or video file to transcribe.",
          enumValues: nil, defaultValue: nil),
        "model": MCPPropertySchema(
          type: "string",
          description: "Catalog model identifier (default: tiny).",
          enumValues: nil, defaultValue: AnyCodable("tiny")),
        "language": MCPPropertySchema(
          type: "string",
          description: "Language code (e.g. 'en', 'de') or 'auto' for detection.",
          enumValues: nil, defaultValue: AnyCodable("auto")),
        "title": MCPPropertySchema(
          type: "string",
          description: "Optional title for the meeting artifact.",
          enumValues: nil, defaultValue: nil),
      ],
      required: ["file"]
    )
  )

  static let meetingJobStatus = MCPToolSchema(
    name: "meeting_job_status",
    description: "Get the status of a transcription job by meeting or job UUID.",
    inputSchema: MCPInputSchema(
      type: "object",
      properties: [
        "job_id": MCPPropertySchema(
          type: "string",
          description: "The meeting UUID or job UUID.",
          enumValues: nil, defaultValue: nil),
      ],
      required: ["job_id"]
    )
  )

  static let meetingJobCancel = MCPToolSchema(
    name: "meeting_job_cancel",
    description:
      "Request cooperative cancellation of a transcription job. Reduces exposure by stopping early.",
    inputSchema: MCPInputSchema(
      type: "object",
      properties: [
        "job_id": MCPPropertySchema(
          type: "string",
          description: "The meeting UUID or job UUID.",
          enumValues: nil, defaultValue: nil),
      ],
      required: ["job_id"]
    )
  )

  static let meetingExport = MCPToolSchema(
    name: "meeting_export",
    description:
      "Export a completed meeting's transcript in markdown, txt, json, jsonl, srt, or vtt format.",
    inputSchema: MCPInputSchema(
      type: "object",
      properties: [
        "meeting_id": MCPPropertySchema(
          type: "string",
          description: "The meeting UUID.",
          enumValues: nil, defaultValue: nil),
        "format": MCPPropertySchema(
          type: "string",
          description: "Export format.",
          enumValues: ["markdown", "txt", "json", "jsonl", "srt", "vtt"],
          defaultValue: AnyCodable("markdown")),
        "segments": MCPPropertySchema(
          type: "string",
          description: "Segment source: raw or edited.",
          enumValues: ["raw", "edited"],
          defaultValue: nil),
      ],
      required: ["meeting_id", "format"]
    )
  )

  static let meetingRecordingStatus = MCPToolSchema(
    name: "meeting_recording_status",
    description: "Check the status of an active recording session.",
    inputSchema: MCPInputSchema(
      type: "object",
      properties: [:],
      required: []
    )
  )

  static let meetingRecordingRequest = MCPToolSchema(
    name: "meeting_recording_request",
    description:
      "Request to start a recording. Returns confirmation_required because only the human-facing agent can authorize capture. Never falls back to direct CLI capture.",
    inputSchema: MCPInputSchema(
      type: "object",
      properties: [
        "purpose": MCPPropertySchema(
          type: "string",
          description: "The purpose/name of the meeting to record.",
          enumValues: nil, defaultValue: nil),
      ],
      required: ["purpose"]
    )
  )

  static let meetingRecordingStop = MCPToolSchema(
    name: "meeting_recording_stop",
    description:
      "Stop an active recording. Stopping reduces exposure and may proceed without additional confirmation.",
    inputSchema: MCPInputSchema(
      type: "object",
      properties: [:],
      required: []
    )
  )
}
