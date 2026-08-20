import Foundation
import SpeakerKit
import XCTest

@testable import SymMeetCore
@testable import SymMeetSpeakerKit

final class SpeakerTurnMapperTests: XCTestCase {
  func testFromUpstreamIDsProducesDeterministicMapping() {
    let mapper = SpeakerTurnMapper.fromUpstreamIDs([2, 0, 1])
    XCTAssertEqual(mapper.upstreamToLocal.count, 3)
    XCTAssertEqual(mapper.upstreamToLocal[0].local, "speaker_0")
    XCTAssertEqual(mapper.upstreamToLocal[1].local, "speaker_1")
    XCTAssertEqual(mapper.upstreamToLocal[2].local, "speaker_2")
    XCTAssertEqual(mapper.upstreamToLocal[0].upstream, 0)
    XCTAssertEqual(mapper.upstreamToLocal[1].upstream, 1)
    XCTAssertEqual(mapper.upstreamToLocal[2].upstream, 2)
  }

  func testMapToTurnsConvertsUpstreamSegments() throws {
    let mapper = SpeakerTurnMapper.fromUpstreamIDs([0, 1])
    let meetingID = UUID()

    let upstreamSegments = [
      SpeakerSegment(speaker: .speakerId(0), startTime: 0.0, endTime: 1.5, frameRate: 16_000),
      SpeakerSegment(speaker: .speakerId(1), startTime: 1.0, endTime: 3.0, frameRate: 16_000),
    ]

    let turns = mapper.mapToTurns(upstreamSegments, meetingID: meetingID)
    XCTAssertEqual(turns.count, 2)
    XCTAssertEqual(turns[0].speakerID, "speaker_0")
    XCTAssertEqual(turns[0].startMS, 0)
    XCTAssertEqual(turns[0].endMS, 1_500)
    XCTAssertEqual(turns[1].speakerID, "speaker_1")
    XCTAssertEqual(turns[1].startMS, 1_000)
    XCTAssertEqual(turns[1].endMS, 3_000)
  }

  func testMapToTurnsHandlesNoMatchAsUnknown() {
    let mapper = SpeakerTurnMapper.fromUpstreamIDs([0])
    let meetingID = UUID()

    let upstreamSegments = [
      SpeakerSegment(speaker: .noMatch, startTime: 0.0, endTime: 1.0, frameRate: 16_000)
    ]

    let turns = mapper.mapToTurns(upstreamSegments, meetingID: meetingID)
    XCTAssertEqual(turns.count, 1)
    XCTAssertEqual(turns[0].speakerID, SpeakerAlignment.unknownSpeakerID)
    XCTAssertEqual(turns[0].confidence, 0)
  }

  func testMapToTurnsHandlesMultipleSpeakerInfoAsUnknown() {
    let mapper = SpeakerTurnMapper.fromUpstreamIDs([0])
    let meetingID = UUID()

    let upstreamSegments = [
      SpeakerSegment(
        speaker: .multiple([0, 1]), startTime: 0.0, endTime: 1.0, frameRate: 16_000)
    ]

    let turns = mapper.mapToTurns(upstreamSegments, meetingID: meetingID)
    XCTAssertEqual(turns.count, 1)
    XCTAssertEqual(turns[0].speakerID, SpeakerAlignment.unknownSpeakerID)
  }

  func testMapToTurnsSkipsZeroDurationSegments() {
    let mapper = SpeakerTurnMapper.fromUpstreamIDs([0])
    let meetingID = UUID()

    let upstreamSegments = [
      SpeakerSegment(speaker: .speakerId(0), startTime: 1.0, endTime: 1.0, frameRate: 16_000)
    ]

    let turns = mapper.mapToTurns(upstreamSegments, meetingID: meetingID)
    XCTAssertEqual(turns.count, 0)
  }
}

final class TrackAwareDiarizerTests: XCTestCase {
  func testImportedMixedMapsAllSpeakers() throws {
    let diarizer = TrackAwareDiarizer()
    let meetingID = UUID()
    let request = DiarizationRequest(
      sourceKind: .importedMixed,
      meetingID: meetingID,
      audioSamples: Array(repeating: 0, count: 16_000),
      durationMS: 1_000)

    let upstreamSegments = [
      SpeakerSegment(
        speaker: .speakerId(0), startTime: 0.0, endTime: 0.5,
        frameRate: 16_000),
      SpeakerSegment(
        speaker: .speakerId(1), startTime: 0.3, endTime: 1.0,
        frameRate: 16_000),
    ]

    let result = diarizer.process(
      request, upstreamSegments: upstreamSegments)
    XCTAssertEqual(result.speakerCount, 2)
    XCTAssertEqual(result.turns.count, 2)

    let speakerIDs = Set(result.turns.map(\.speakerID))
    XCTAssertTrue(speakerIDs.contains("speaker_0"))
    XCTAssertTrue(speakerIDs.contains("speaker_1"))
  }

  func testNativeDualTrackReservesLocalSpeaker() throws {
    let diarizer = TrackAwareDiarizer()
    let meetingID = UUID()
    let request = DiarizationRequest(
      sourceKind: .nativeDualTrack,
      meetingID: meetingID,
      audioSamples: Array(repeating: 0, count: 16_000),
      microphoneSamples: Array(repeating: 0, count: 16_000),
      durationMS: 1_000)

    let upstreamSegments = [
      SpeakerSegment(
        speaker: .speakerId(0), startTime: 0.0, endTime: 0.5,
        frameRate: 16_000),
      SpeakerSegment(
        speaker: .speakerId(1), startTime: 0.3, endTime: 1.0,
        frameRate: 16_000),
    ]

    let result = diarizer.process(request, upstreamSegments: upstreamSegments)
    XCTAssertEqual(result.speakerCount, 3)

    let localTurns = result.turns.filter { $0.speakerID == LocalSpeaker.reservedID }
    XCTAssertEqual(localTurns.count, 1)
    XCTAssertEqual(localTurns[0].startMS, 0)
    XCTAssertEqual(localTurns[0].endMS, 1_000)
  }

  func testNativeDualTrackWithoutMicOmitsLocalSpeaker() throws {
    let diarizer = TrackAwareDiarizer()
    let meetingID = UUID()
    let request = DiarizationRequest(
      sourceKind: .nativeDualTrack,
      meetingID: meetingID,
      audioSamples: Array(repeating: 0, count: 16_000),
      microphoneSamples: nil,
      durationMS: 1_000)

    let upstreamSegments = [
      SpeakerSegment(speaker: .speakerId(0), startTime: 0.0, endTime: 1.0, frameRate: 16_000)
    ]

    let result = diarizer.process(request, upstreamSegments: upstreamSegments)
    let localTurns = result.turns.filter { $0.speakerID == LocalSpeaker.reservedID }
    XCTAssertEqual(localTurns.count, 0)
  }

  func testRTTMContainsMeetingID() throws {
    let diarizer = TrackAwareDiarizer()
    let meetingID = UUID()
    let request = DiarizationRequest(
      sourceKind: .importedMixed,
      meetingID: meetingID,
      audioSamples: Array(repeating: 0, count: 16_000),
      durationMS: 1_000)

    let upstreamSegments = [
      SpeakerSegment(speaker: .speakerId(0), startTime: 0.0, endTime: 1.0, frameRate: 16_000)
    ]

    let result = diarizer.process(request, upstreamSegments: upstreamSegments)
    XCTAssertEqual(result.rttmLines.count, 1)
    XCTAssertTrue(result.rttmLines[0].contains(meetingID.uuidString.lowercased()))
  }
}
