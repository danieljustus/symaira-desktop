import XCTest

@testable import SymMeetCore

final class IntegrationFixtureTests: XCTestCase {

  func testMeetingCompleteManifestRoundTrips() throws {
    let fixture = Bundle.module.url(
      forResource: "manifest", withExtension: "json",
      subdirectory: "integration/meeting-complete")!
    let data = try Data(contentsOf: fixture)
    let manifest = try ContractCodec.decoder().decode(MeetingManifest.self, from: data)

    XCTAssertEqual(manifest.schemaVersion, 1)
    XCTAssertEqual(manifest.source, .imported)
    XCTAssertEqual(manifest.job?.state, .completed)
    XCTAssertEqual(manifest.language, "en")
    XCTAssertEqual(manifest.consent.status, .required)
    XCTAssertEqual(manifest.retention.policy, .keep)
    XCTAssertEqual(manifest.audioTracks.count, 1)
    XCTAssertEqual(manifest.audioTracks[0].kind, .original)
  }

  func testMeetingCompleteSegmentsRoundTrip() throws {
    let fixture = Bundle.module.url(
      forResource: "segments", withExtension: "jsonl",
      subdirectory: "integration/meeting-complete")!
    let data = try Data(contentsOf: fixture)
    let segments = try data.split(separator: 0x0A).map { line in
      try ContractCodec.decoder().decode(Segment.self, from: Data(line))
    }

    XCTAssertEqual(segments.count, 2)
    XCTAssertEqual(segments[0].speakerID, "speaker_0")
    XCTAssertEqual(segments[0].startMS, 0)
    XCTAssertEqual(segments[0].endMS, 3000)
    XCTAssertEqual(segments[0].engineText, "Good morning everyone.")
    XCTAssertEqual(segments[1].startMS, 3500)
  }

  func testMeetingCompleteSchemaVersionIsOne() throws {
    let fixture = Bundle.module.url(
      forResource: "manifest", withExtension: "json",
      subdirectory: "integration/meeting-complete")!
    let data = try Data(contentsOf: fixture)
    let manifest = try ContractCodec.decoder().decode(MeetingManifest.self, from: data)

    XCTAssertEqual(
      manifest.schemaVersion, MeetingManifest.supportedSchemaVersion,
      "Fixture schema version must match the supported version")
  }

  func testMeetingCompleteExportRendersMarkdown() throws {
    let manifestFixture = Bundle.module.url(
      forResource: "manifest", withExtension: "json",
      subdirectory: "integration/meeting-complete")!
    let segmentsFixture = Bundle.module.url(
      forResource: "segments", withExtension: "jsonl",
      subdirectory: "integration/meeting-complete")!

    let manifestData = try Data(contentsOf: manifestFixture)
    let manifest = try ContractCodec.decoder().decode(MeetingManifest.self, from: manifestData)

    let segmentsData = try Data(contentsOf: segmentsFixture)
    let segments = try segmentsData.split(separator: 0x0A).map { line in
      try ContractCodec.decoder().decode(Segment.self, from: Data(line))
    }

    let markdown = try TranscriptRenderer.render(
      manifest: manifest,
      segments: segments,
      segmentSource: .raw,
      format: .markdown
    )

    XCTAssertFalse(markdown.isEmpty, "Markdown export must not be empty")
    XCTAssertTrue(
      markdown.contains("Good morning everyone."),
      "Markdown must contain segment text")
  }
}
