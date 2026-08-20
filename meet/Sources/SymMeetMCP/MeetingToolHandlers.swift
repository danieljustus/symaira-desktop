import Foundation
import SymMeetCore

// MARK: - Meeting list handler

struct MeetingListHandler: MCPToolHandler {
  let toolName = "meeting_list"
  let store: MeetingStore

  func execute(args: [String: AnyCodable]) async throws -> MCPToolResult {
    let result = try await store.list()

    let output = MeetingListOutput(
      meetings: result.meetings.map { manifest in
        MeetingSummary(
          meetingID: manifest.meetingID.uuidString.lowercased(),
          source: manifest.source.rawValue,
          createdAt: manifest.createdAt,
          language: manifest.language,
          jobState: manifest.job?.state.rawValue
        )
      },
      diagnostics: result.diagnostics.map { diag in
        DiagnosticOutput(meetingID: diag.meetingID, code: diag.code.rawValue)
      }
    )

    return try MCPToolResult.json(output)
  }
}

// MARK: - Meeting get handler

struct MeetingGetHandler: MCPToolHandler {
  let toolName = "meeting_get"
  let maxSegments = 500
  let store: MeetingStore

  func execute(args: [String: AnyCodable]) async throws -> MCPToolResult {
    guard let meetingID = args["meeting_id"]?.asString else {
      return .error("Missing required parameter: meeting_id")
    }

    let manifest = try await store.load(meetingID: meetingID)

    let includeSegments = args["include_segments"]?.asBool ?? false
    let segmentLimit = min(args["segment_limit"]?.asInt ?? 50, maxSegments)

    var output = MeetingGetOutput(
      meetingID: manifest.meetingID.uuidString.lowercased(),
      source: manifest.source.rawValue,
      createdAt: manifest.createdAt,
      updatedAt: manifest.updatedAt,
      language: manifest.language,
      job: manifest.job.map {
        JobOutput(jobID: $0.jobID.uuidString.lowercased(), state: $0.state.rawValue)
      },
      consent: ConsentOutput(status: manifest.consent.status.rawValue),
      retention: RetentionOutput(policy: manifest.retention.policy.rawValue)
    )

    if includeSegments {
      let bounded = try await store.rawSegments(meetingID: meetingID, limit: segmentLimit)
      output.segments = bounded.segments.map { seg in
        SegmentOutput(
          segmentID: seg.segmentID.uuidString.lowercased(),
          speakerID: seg.speakerID,
          startMS: seg.startMS,
          endMS: seg.endMS,
          text: seg.editedText ?? seg.engineText,
          revision: seg.revision.rawValue
        )
      }
      output.segmentCount = bounded.totalCount
      output.segmentLimit = segmentLimit
    }

    return try MCPToolResult.json(output)
  }
}

// MARK: - Meeting transcribe handler

struct MeetingTranscribeHandler: MCPToolHandler {
  let toolName = "meeting_transcribe"
  let store: MeetingStore
  let dataRoot: URL
  /// The model store used for the "is this model installed" gate. Injectable
  /// for tests; defaults to the user's model directory.
  let modelStore: ModelStore
  /// The transcription engine used for the pipeline run. `nil` means the
  /// built-in stub engine (which produces no segments); injectable for tests.
  let engine: (any TranscriptionEngine)?

  init(
    store: MeetingStore,
    dataRoot: URL,
    modelStore: ModelStore = ModelStore(),
    engine: (any TranscriptionEngine)? = nil
  ) {
    self.store = store
    self.dataRoot = dataRoot
    self.modelStore = modelStore
    self.engine = engine
  }

  func execute(args: [String: AnyCodable]) async throws -> MCPToolResult {
    guard let file = args["file"]?.asString else {
      return .error("Missing required parameter: file")
    }

    guard FileManager.default.fileExists(atPath: file) else {
      return .error("File not found: \(file)")
    }

    let model = args["model"]?.asString ?? "tiny"

    do {
      _ = try await modelStore.verify(id: model)
    } catch ModelError.modelNotInstalled {
      return .error("Model '\(model)' is not installed. Run: symmeet model install \(model)")
    } catch {
      return .error("Model verification failed: \(error.localizedDescription)")
    }

    let pipeline = TranscriptionPipeline(
      dataRoot: dataRoot,
      meetingStore: store
    )
    let sourceURL = URL(fileURLWithPath: file)
    let languageValue =
      (args["language"]?.asString ?? "auto") == "auto" ? nil : args["language"]?.asString
    let title = args["title"]?.asString

    do {
      let outcome = try await pipeline.run(
        TranscriptionRequestOptions(
          sourceURL: sourceURL,
          title: title,
          language: languageValue,
          modelID: model,
          modelVersion: "",
          engineID: "whisperkit"
        ),
        engine: engine ?? StubEngine()
      )

      let result = TranscribeResultOutput(
        meetingID: outcome.meetingID.uuidString.lowercased(),
        jobID: outcome.jobID.uuidString.lowercased(),
        state: outcome.status.rawValue,
        segmentCount: outcome.segmentCount,
        language: outcome.language
      )
      return try MCPToolResult.json(result)
    } catch {
      return .error("Transcription failed: \(error.localizedDescription)")
    }
  }
}

// MARK: - Meeting export handler

struct MeetingExportHandler: MCPToolHandler {
  let toolName = "meeting_export"
  let store: MeetingStore

  func execute(args: [String: AnyCodable]) async throws -> MCPToolResult {
    guard let meetingID = args["meeting_id"]?.asString else {
      return .error("Missing required parameter: meeting_id")
    }
    guard let formatStr = args["format"]?.asString,
      let format = ExportFormat(rawValue: formatStr)
    else {
      return .error(
        "Invalid format. Supported: \(ExportFormat.allCases.map(\.rawValue).joined(separator: ", "))"
      )
    }

    let manifest = try await store.load(meetingID: meetingID)

    if manifest.job?.state != .completed {
      return .error(
        "Meeting transcription is not complete (state: \(manifest.job?.state.rawValue ?? "not_started"))."
      )
    }

    var requestedSource: ExportSegmentSource?
    if let segmentsStr = args["segments"]?.asString {
      guard let parsed = ExportSegmentSource(rawValue: segmentsStr) else {
        return .error("Invalid segment source. Supported: raw, edited.")
      }
      requestedSource = parsed
    }

    let segments: [Segment]
    let segmentSource: ExportSegmentSource

    if let requested = requestedSource {
      switch requested {
      case .raw:
        segments = try await store.rawSegments(meetingID: meetingID)
        segmentSource = .raw
      case .edited:
        let edited = try await store.editedSegments(meetingID: meetingID)
        guard !edited.isEmpty else {
          return .error("Edited segments are unavailable for this meeting.")
        }
        segments = edited
        segmentSource = .edited
      }
    } else {
      let edited = try await store.editedSegments(meetingID: meetingID)
      if !edited.isEmpty {
        segments = edited
        segmentSource = .edited
      } else {
        segments = try await store.rawSegments(meetingID: meetingID)
        segmentSource = .raw
      }
    }

    let content = try TranscriptRenderer.render(
      manifest: manifest,
      segments: segments,
      segmentSource: segmentSource,
      format: format,
      options: TranscriptRenderer.Options(withTimestamps: false)
    )

    let output = ExportOutput(
      meetingID: meetingID.lowercased(),
      format: format.rawValue,
      segmentSource: segmentSource.rawValue,
      segmentCount: segments.count,
      content: content
    )
    return try MCPToolResult.json(output)
  }
}

// MARK: - Stub engine for MCP transcription

/// A minimal engine stub used by the MCP transcribe handler.
/// This satisfies the TranscriptionEngine Actor protocol without pulling
/// in SymMeetWhisperKit. The pipeline will create the meeting artifact
/// and job but the engine produces no segments.
private actor StubEngine: TranscriptionEngine {
  let engineID = "stub"
  let capabilities = EngineCapabilities(
    languages: [],
    supportsAutoDetection: false,
    supportsWordTimestamps: false,
    supportsSegmentTimestamps: false,
    supportsStreaming: false,
    supportsDiarization: false,
    requiredArchitectures: []
  )

  func transcribe(
    _ request: TranscriptionRequest
  ) -> AsyncThrowingStream<TranscriptionEvent, Error> {
    AsyncThrowingStream { continuation in
      continuation.finish()
    }
  }
}

// MARK: - Output types

private struct MeetingListOutput: Encodable {
  let meetings: [MeetingSummary]
  let diagnostics: [DiagnosticOutput]
}

private struct MeetingSummary: Encodable {
  let meetingID: String
  let source: String
  let createdAt: Date
  let language: String?
  let jobState: String?

  private enum CodingKeys: String, CodingKey {
    case meetingID = "meeting_id"
    case source
    case createdAt = "created_at"
    case language
    case jobState = "job_state"
  }
}

private struct DiagnosticOutput: Encodable {
  let meetingID: String
  let code: String

  private enum CodingKeys: String, CodingKey {
    case meetingID = "meeting_id"
    case code
  }
}

private struct MeetingGetOutput: Encodable {
  let meetingID: String
  let source: String
  let createdAt: Date
  let updatedAt: Date
  let language: String?
  let job: JobOutput?
  let consent: ConsentOutput
  let retention: RetentionOutput
  var segments: [SegmentOutput]?
  var segmentCount: Int?
  var segmentLimit: Int?

  private enum CodingKeys: String, CodingKey {
    case meetingID = "meeting_id"
    case source
    case createdAt = "created_at"
    case updatedAt = "updated_at"
    case language, job, consent, retention, segments
    case segmentCount = "segment_count"
    case segmentLimit = "segment_limit"
  }
}

private struct JobOutput: Encodable {
  let jobID: String
  let state: String

  private enum CodingKeys: String, CodingKey {
    case jobID = "job_id"
    case state
  }
}

private struct ConsentOutput: Encodable {
  let status: String
}

private struct RetentionOutput: Encodable {
  let policy: String
}

private struct SegmentOutput: Encodable {
  let segmentID: String
  let speakerID: String
  let startMS: Int
  let endMS: Int
  let text: String
  let revision: String

  private enum CodingKeys: String, CodingKey {
    case segmentID = "segment_id"
    case speakerID = "speaker_id"
    case startMS = "start_ms"
    case endMS = "end_ms"
    case text, revision
  }
}

private struct TranscribeResultOutput: Encodable {
  let meetingID: String
  let jobID: String
  let state: String
  let segmentCount: Int
  let language: String?

  private enum CodingKeys: String, CodingKey {
    case meetingID = "meeting_id"
    case jobID = "job_id"
    case state
    case segmentCount = "segment_count"
    case language
  }
}

private struct ExportOutput: Encodable {
  let meetingID: String
  let format: String
  let segmentSource: String
  let segmentCount: Int
  let content: String

  private enum CodingKeys: String, CodingKey {
    case meetingID = "meeting_id"
    case format
    case segmentSource = "segment_source"
    case segmentCount = "segment_count"
    case content
  }
}
