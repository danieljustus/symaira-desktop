import Foundation
import XCTest

@testable import SymMeetCore

final class ContractFixtureTests: XCTestCase {
  func testMeetingFixtureRoundTripsUnknownAdditiveFields() throws {
    let manifest = try ContractCodec.decoder().decode(
      MeetingManifest.self, from: fixture("meeting-valid"))

    XCTAssertEqual(manifest.schemaVersion, 1)
    XCTAssertEqual(manifest.additionalFields["future_additive"], .string("preserve-me"))

    let rewritten = try ContractCodec.encoder().encode(manifest)
    let json = try XCTUnwrap(JSONSerialization.jsonObject(with: rewritten) as? [String: Any])
    XCTAssertEqual(json["future_additive"] as? String, "preserve-me")
  }

  func testSegmentFixtureDistinguishesEngineAndEditedText() throws {
    let segment = try ContractCodec.decoder().decode(Segment.self, from: fixture("segment-valid"))

    XCTAssertEqual(segment.engineText, "Original engine words.")
    XCTAssertEqual(segment.editedText, "Corrected user-visible words.")
    XCTAssertEqual(segment.revision, .userCorrected)
  }

  func testEventFixtureRoundTrips() throws {
    let event = try ContractCodec.decoder().decode(EventEnvelope.self, from: fixture("event-valid"))

    XCTAssertEqual(event.type, .lifecycleChanged)
    XCTAssertEqual(event.additionalFields["future_metadata"], .bool(true))
  }

  func testUnsupportedSchemaVersionFailsWithTypedError() {
    XCTAssertThrowsError(
      try ContractCodec.decoder().decode(
        MeetingManifest.self, from: fixture("meeting-unsupported-version"))
    ) { error in
      XCTAssertEqual(error as? ContractError, .unsupportedSchemaVersion(2))
    }
  }

  func testInvalidSegmentTimeRangeFailsClosed() {
    XCTAssertThrowsError(
      try ContractCodec.decoder().decode(Segment.self, from: fixture("segment-invalid-time-range"))
    ) { error in
      XCTAssertEqual(error as? ContractError, .invalidTimeRange(startMS: 500, endMS: 100))
    }
  }

  func testInvalidAnonymousSpeakerIdentifierFailsClosed() {
    XCTAssertThrowsError(
      try Segment(
        segmentID: UUID(),
        trackID: UUID(),
        speakerID: "Taylor",
        startMS: 0,
        endMS: 1,
        engineText: "No personally identifying diarization label."
      )
    ) { error in
      XCTAssertEqual(error as? ContractError, .invalidIdentifier("speaker_id"))
    }
  }

  func testImpossibleLifecycleTransitionFailsClosed() {
    XCTAssertThrowsError(try LifecycleTransition(from: .completed, to: .processing)) { error in
      XCTAssertEqual(
        error as? ContractError,
        .invalidStateTransition(from: .completed, to: .processing)
      )
    }
  }

  func testMarkdownProjectionUsesStableSections() {
    let projection = MarkdownProjection(
      summary: "Discussed the artifact contract.",
      decisions: ["Keep raw evidence immutable."],
      actionItems: ["Implement the store."],
      participants: ["person:abc"],
      transcript: "Corrected user-visible text."
    )

    XCTAssertTrue(projection.markdown.contains("## Summary"))
    XCTAssertTrue(projection.markdown.contains("## Decisions"))
    XCTAssertTrue(projection.markdown.contains("## Action Items"))
    XCTAssertTrue(projection.markdown.contains("## Participants"))
    XCTAssertTrue(projection.markdown.contains("## Transcript"))
  }

  private func fixture(_ name: String) -> Data {
    let url = Bundle.module.url(
      forResource: name,
      withExtension: "json",
      subdirectory: "contracts"
    )!
    return try! Data(contentsOf: url)
  }
}
