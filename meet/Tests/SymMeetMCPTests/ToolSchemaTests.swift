import XCTest

@testable import SymMeetMCP

final class ToolSchemaTests: XCTestCase {

  // MARK: - Tool registry

  func testAllNineToolsRegistered() {
    XCTAssertEqual(MCPToolRegistry.tools.count, 9)
  }

  func testToolNamesAreSnakeCase() {
    for tool in MCPToolRegistry.tools {
      let name = tool.name
      XCTAssertFalse(name.isEmpty, "Tool name must not be empty")
      XCTAssertFalse(
        name.contains("-"),
        "Tool name '\(name)' must use snake_case, not kebab-case")
      XCTAssertFalse(
        name.first?.isUppercase ?? false,
        "Tool name '\(name)' must start with lowercase")
      XCTAssertFalse(
        name.last == "_",
        "Tool name '\(name)' must not end with underscore")
    }
  }

  func testToolNamesAreUnique() {
    let names = MCPToolRegistry.tools.map(\.name)
    let uniqueNames = Set(names)
    XCTAssertEqual(
      names.count, uniqueNames.count,
      "Tool names must be unique")
  }

  func testToolInputSchemasAreObject() {
    for tool in MCPToolRegistry.tools {
      XCTAssertEqual(
        tool.inputSchema.type, "object",
        "Tool '\(tool.name)' input schema must be type 'object'")
    }
  }

  // MARK: - Individual tool schemas

  func testMeetingListTool() {
    let tool = MCPToolRegistry.tools.first { $0.name == "meeting_list" }
    XCTAssertNotNil(tool)
    XCTAssertEqual(tool?.inputSchema.properties.count, 0)
    XCTAssertEqual(tool?.inputSchema.required.count, 0)
  }

  func testMeetingGetTool() {
    let tool = MCPToolRegistry.tools.first { $0.name == "meeting_get" }
    XCTAssertNotNil(tool)
    XCTAssertNotNil(tool?.inputSchema.properties["meeting_id"])
    XCTAssertEqual(tool?.inputSchema.required, ["meeting_id"])

    let includeSegments = tool?.inputSchema.properties["include_segments"]
    XCTAssertNotNil(includeSegments)
    XCTAssertEqual(includeSegments?.type, "boolean")

    let segmentLimit = tool?.inputSchema.properties["segment_limit"]
    XCTAssertNotNil(segmentLimit)
    XCTAssertEqual(segmentLimit?.type, "integer")
  }

  func testMeetingTranscribeTool() {
    let tool = MCPToolRegistry.tools.first { $0.name == "meeting_transcribe" }
    XCTAssertNotNil(tool)
    XCTAssertEqual(tool?.inputSchema.required, ["file"])
    XCTAssertNotNil(tool?.inputSchema.properties["model"])
    XCTAssertNotNil(tool?.inputSchema.properties["language"])
  }

  func testMeetingJobStatusTool() {
    let tool = MCPToolRegistry.tools.first { $0.name == "meeting_job_status" }
    XCTAssertNotNil(tool)
    XCTAssertEqual(tool?.inputSchema.required, ["job_id"])
  }

  func testMeetingJobCancelTool() {
    let tool = MCPToolRegistry.tools.first { $0.name == "meeting_job_cancel" }
    XCTAssertNotNil(tool)
    XCTAssertEqual(tool?.inputSchema.required, ["job_id"])
  }

  func testMeetingExportTool() {
    let tool = MCPToolRegistry.tools.first { $0.name == "meeting_export" }
    XCTAssertNotNil(tool)
    XCTAssertEqual(tool?.inputSchema.required, ["meeting_id", "format"])

    let formatProp = tool?.inputSchema.properties["format"]
    XCTAssertNotNil(formatProp)
    XCTAssertEqual(
      formatProp?.enumValues,
      ["markdown", "txt", "json", "jsonl", "srt", "vtt"])
  }

  func testMeetingRecordingStatusTool() {
    let tool = MCPToolRegistry.tools.first { $0.name == "meeting_recording_status" }
    XCTAssertNotNil(tool)
    XCTAssertEqual(tool?.inputSchema.properties.count, 0)
  }

  func testMeetingRecordingRequestTool() {
    let tool = MCPToolRegistry.tools.first { $0.name == "meeting_recording_request" }
    XCTAssertNotNil(tool)
    XCTAssertEqual(tool?.inputSchema.required, ["purpose"])
  }

  func testMeetingRecordingStopTool() {
    let tool = MCPToolRegistry.tools.first { $0.name == "meeting_recording_stop" }
    XCTAssertNotNil(tool)
    XCTAssertEqual(tool?.inputSchema.properties.count, 0)
  }

  // MARK: - Schema serialization

  func testToolSchemasAreCodable() throws {
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.sortedKeys]

    for tool in MCPToolRegistry.tools {
      let data = try encoder.encode(tool)
      let json = String(decoding: data, as: UTF8.self)

      XCTAssertTrue(
        json.contains("\"name\""),
        "Tool schema must encode name")
      XCTAssertTrue(
        json.contains("\"description\""),
        "Tool schema must encode description")
      XCTAssertTrue(
        json.contains("\"input_schema\""),
        "Tool schema must encode input_schema")

      let decoded = try JSONDecoder().decode(MCPToolSchema.self, from: data)
      XCTAssertEqual(decoded.name, tool.name)
    }
  }
}
