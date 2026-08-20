import Foundation
import XCTest

@testable import SymMeetCore
@testable import SymMeetSpeakerKit

final class SpeakerTurnTests: XCTestCase {
  func testSpeakerTurnEncodesWithSnakeCaseKeys() throws {
    let turn = try SpeakerTurn(
      speakerID: "speaker_0",
      startMS: 0,
      endMS: 1_000,
      confidence: 0.95,
      isOverlapping: false)

    let data = try ContractCodec.encoder().encode(turn)
    let json = String(decoding: data, as: UTF8.self)

    XCTAssertTrue(json.contains("\"speaker_id\""))
    XCTAssertTrue(json.contains("\"start_ms\""))
    XCTAssertTrue(json.contains("\"end_ms\""))
    XCTAssertTrue(json.contains("\"is_overlapping\""))
    XCTAssertTrue(json.contains("\"schema_version\""))
  }

  func testSpeakerTurnRoundTripsThroughCodable() throws {
    let turn = try SpeakerTurn(
      turnID: UUID(),
      speakerID: "speaker_1",
      startMS: 500,
      endMS: 2_500,
      confidence: 0.8,
      isOverlapping: true,
      provenance: .userCorrected)

    let data = try ContractCodec.encoder().encode(turn)
    let decoded = try ContractCodec.decoder().decode(
      SpeakerTurn.self, from: data)

    XCTAssertEqual(decoded.turnID, turn.turnID)
    XCTAssertEqual(decoded.speakerID, turn.speakerID)
    XCTAssertEqual(decoded.startMS, turn.startMS)
    XCTAssertEqual(decoded.endMS, turn.endMS)
    XCTAssertEqual(decoded.confidence, turn.confidence, accuracy: 0.001)
    XCTAssertEqual(decoded.isOverlapping, turn.isOverlapping)
    XCTAssertEqual(decoded.provenance, .userCorrected)
  }

  func testSpeakerTurnRejectsInvalidTimeRange() {
    XCTAssertThrowsError(
      try SpeakerTurn(speakerID: "speaker_0", startMS: 100, endMS: 100)
    ) { error in
      XCTAssertTrue(error is ContractError)
    }
    XCTAssertThrowsError(
      try SpeakerTurn(speakerID: "speaker_0", startMS: -1, endMS: 100)
    ) { error in
      XCTAssertTrue(error is ContractError)
    }
  }

  func testSpeakerTurnRejectsInvalidConfidence() {
    XCTAssertThrowsError(
      try SpeakerTurn(
        speakerID: "speaker_0", startMS: 0, endMS: 100, confidence: -0.1)
    ) { error in
      XCTAssertTrue(error is DiarizationContractError)
    }
    XCTAssertThrowsError(
      try SpeakerTurn(
        speakerID: "speaker_0", startMS: 0, endMS: 100, confidence: 1.5)
    ) { error in
      XCTAssertTrue(error is DiarizationContractError)
    }
  }

  func testSpeakerTurnAcceptsBoundaryConfidence() throws {
    let low = try SpeakerTurn(
      speakerID: "speaker_0", startMS: 0, endMS: 100, confidence: 0)
    XCTAssertEqual(low.confidence, 0)

    let high = try SpeakerTurn(
      speakerID: "speaker_0", startMS: 0, endMS: 100, confidence: 1)
    XCTAssertEqual(high.confidence, 1)
  }

  func testLocalSpeakerReservedID() {
    XCTAssertEqual(LocalSpeaker.reservedID, "speaker_local")
  }

  func testTurnProvenanceRawValues() {
    XCTAssertEqual(TurnProvenance.engine.rawValue, "engine")
    XCTAssertEqual(TurnProvenance.userCorrected.rawValue, "user_corrected")
  }

  func testDiarizationContractErrorDescriptions() {
    let confidence = DiarizationContractError.invalidConfidence(1.5)
    XCTAssertNotNil(confidence.errorDescription)

    let speaker = DiarizationContractError.unknownSpeakerID("foo")
    XCTAssertNotNil(speaker.errorDescription)
  }
}

final class SpeakerAlignmentTests: XCTestCase {
  func testAlignAssignsPrimarySpeakerByLargestOverlap() throws {
    let meetingID = UUID()
    let seg = try Segment(
      segmentID: UUID(),
      trackID: UUID(),
      speakerID: "speaker_0",
      startMS: 100,
      endMS: 500,
      engineText: "hello")

    let turns = try [
      SpeakerTurn(speakerID: "speaker_0", startMS: 0, endMS: 300),
      SpeakerTurn(speakerID: "speaker_1", startMS: 200, endMS: 600),
    ]

    let alignments = try SpeakerAligner.align(
      segments: [seg], turns: turns, meetingID: meetingID)
    XCTAssertEqual(alignments.count, 1)

    let alignment = alignments[0]
    XCTAssertEqual(alignment.segmentID, seg.segmentID)
    XCTAssertEqual(alignment.meetingID, meetingID)
    XCTAssertEqual(alignment.speakerID, "speaker_1")
    XCTAssertGreaterThan(alignment.confidence, 0)
  }

  func testAlignUsesUnknownWhenNoTurnsOverlap() throws {
    let meetingID = UUID()
    let seg = try Segment(
      segmentID: UUID(),
      trackID: UUID(),
      speakerID: "speaker_0",
      startMS: 1000,
      endMS: 2000,
      engineText: "distant")

    let turns = try [
      SpeakerTurn(speakerID: "speaker_0", startMS: 0, endMS: 500)
    ]

    let alignments = try SpeakerAligner.align(
      segments: [seg], turns: turns, meetingID: meetingID)
    XCTAssertEqual(alignments.count, 1)
    XCTAssertEqual(alignments[0].speakerID, SpeakerAlignment.unknownSpeakerID)
    XCTAssertEqual(alignments[0].confidence, 0)
  }

  func testAlignReturnsUnknownForAllSegmentsWhenTurnsEmpty() throws {
    let meetingID = UUID()
    let seg1 = try Segment(
      segmentID: UUID(), trackID: UUID(), speakerID: "speaker_0",
      startMS: 0, endMS: 500, engineText: "a")
    let seg2 = try Segment(
      segmentID: UUID(), trackID: UUID(), speakerID: "speaker_0",
      startMS: 500, endMS: 1000, engineText: "b")

    let alignments = try SpeakerAligner.align(
      segments: [seg1, seg2], turns: [], meetingID: meetingID)
    XCTAssertEqual(alignments.count, 2)
    XCTAssertEqual(alignments[0].speakerID, SpeakerAlignment.unknownSpeakerID)
    XCTAssertEqual(alignments[1].speakerID, SpeakerAlignment.unknownSpeakerID)
  }

  func testAlignReportsOverlappingSpeakers() throws {
    let meetingID = UUID()
    let seg = try Segment(
      segmentID: UUID(), trackID: UUID(), speakerID: "speaker_0",
      startMS: 100, endMS: 500, engineText: "overlap")

    let turns = try [
      SpeakerTurn(speakerID: "speaker_0", startMS: 0, endMS: 600),
      SpeakerTurn(speakerID: "speaker_1", startMS: 200, endMS: 800),
    ]

    let alignments = try SpeakerAligner.align(
      segments: [seg], turns: turns, meetingID: meetingID)
    XCTAssertEqual(alignments.count, 1)
    XCTAssertEqual(alignments[0].overlappingSpeakers, ["speaker_1"])
  }

  func testAlignmentRoundTripsThroughCodable() throws {
    let alignment = try SpeakerAlignment(
      meetingID: UUID(),
      segmentID: UUID(),
      speakerID: "speaker_0",
      confidence: 0.9,
      overlappingSpeakers: ["speaker_1"])

    let data = try ContractCodec.encoder().encode(alignment)
    let decoded = try ContractCodec.decoder().decode(
      SpeakerAlignment.self, from: data)

    XCTAssertEqual(decoded.meetingID, alignment.meetingID)
    XCTAssertEqual(decoded.segmentID, alignment.segmentID)
    XCTAssertEqual(decoded.speakerID, alignment.speakerID)
    XCTAssertEqual(decoded.confidence, alignment.confidence, accuracy: 0.001)
    XCTAssertEqual(decoded.overlappingSpeakers, alignment.overlappingSpeakers)
  }
}
