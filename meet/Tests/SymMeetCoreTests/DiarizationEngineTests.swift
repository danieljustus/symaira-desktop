import Foundation
import XCTest

@testable import SymMeetCore

final class DiarizationEngineContractTests: XCTestCase {
  func testDiarizationRequestInitializes() {
    let request = DiarizationRequest(
      sourceKind: .importedMixed,
      meetingID: UUID(),
      audioSamples: [0, 0, 0],
      numberOfSpeakers: 2,
      durationMS: 3_000)

    XCTAssertEqual(request.sourceKind, .importedMixed)
    XCTAssertEqual(request.audioSamples.count, 3)
    XCTAssertEqual(request.numberOfSpeakers, 2)
    XCTAssertEqual(request.durationMS, 3_000)
    XCTAssertNil(request.microphoneSamples)
  }

  func testDiarizationResultInitializes() {
    let meetingID = UUID()
    let turns = try! [
      SpeakerTurn(speakerID: "speaker_0", startMS: 0, endMS: 1_000),
      SpeakerTurn(speakerID: "speaker_1", startMS: 500, endMS: 2_000),
    ]
    let result = DiarizationOutput(
      meetingID: meetingID,
      turns: turns,
      speakerCount: 2,
      rttmLines: ["SPEAKER test 1 0.000 1.000"])

    XCTAssertEqual(result.meetingID, meetingID)
    XCTAssertEqual(result.turns.count, 2)
    XCTAssertEqual(result.speakerCount, 2)
    XCTAssertEqual(result.rttmLines.count, 1)
  }

  func testDiarizationWarningEncodes() throws {
    let warning = DiarizationWarning(code: "low_confidence", message: "Speaker count uncertain")
    let data = try ContractCodec.encoder().encode(warning)
    let json = String(decoding: data, as: UTF8.self)

    XCTAssertTrue(json.contains("low_confidence"))
    XCTAssertTrue(json.contains("Speaker count uncertain"))
  }

  func testDiarizationOutcomeWithResult() {
    let meetingID = UUID()
    let turns = try! [
      SpeakerTurn(speakerID: "speaker_0", startMS: 0, endMS: 1_000)
    ]
    let result = DiarizationOutput(meetingID: meetingID, turns: turns, speakerCount: 1)
    let outcome = DiarizationOutcome(result: result)

    XCTAssertNotNil(outcome.result)
    XCTAssertNil(outcome.warning)
  }

  func testDiarizationOutcomeWithWarning() {
    let warning = DiarizationWarning(code: "engine_failed", message: "Diarization failed")
    let outcome = DiarizationOutcome(result: nil, warning: warning)

    XCTAssertNil(outcome.result)
    XCTAssertNotNil(outcome.warning)
  }

  func testDiarizationSourceKindRawValues() {
    XCTAssertEqual(DiarizationSourceKind.importedMixed.rawValue, "imported_mixed")
    XCTAssertEqual(DiarizationSourceKind.nativeDualTrack.rawValue, "native_dual_track")
  }

  func testUnknownSpeakerIDSentinel() {
    XCTAssertEqual(SpeakerAlignment.unknownSpeakerID, "speaker_unknown")
  }
}
