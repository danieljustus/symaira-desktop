import Foundation
import XCTest

@testable import SymMeetCore

final class SpeakerEditorTests: XCTestCase {
  private let meetingID = UUID()
  private let knownSpeakerIDs: Set<String> = ["speaker_0", "speaker_1", "speaker_2"]
  private let knownSegmentIDs: Set<UUID> = [UUID(), UUID(), UUID()]

  private var segmentIDs: [UUID] {
    Array(knownSegmentIDs).sorted(by: { $0.uuidString < $1.uuidString })
  }

  // MARK: - Replay

  func testReplayEmptyEventsReturnsEmptyMap() throws {
    let editor = SpeakerEditor()
    let map = try editor.replay(
      events: [],
      knownSpeakerIDs: knownSpeakerIDs,
      knownSegmentIDs: knownSegmentIDs,
      meetingID: meetingID)

    XCTAssertEqual(map.labels, [:])
    XCTAssertEqual(map.mergedSpeakers, [:])
    XCTAssertEqual(map.splitSegments, [:])
    XCTAssertEqual(map.lastEditSequence, 0)
  }

  func testReplayLabelEdit() throws {
    let editor = SpeakerEditor()
    let event = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .label,
      speakerID: "speaker_0",
      label: "Alice",
      sequenceNumber: 1)

    let map = try editor.replay(
      events: [event],
      knownSpeakerIDs: knownSpeakerIDs,
      knownSegmentIDs: knownSegmentIDs,
      meetingID: meetingID)

    XCTAssertEqual(map.labels["speaker_0"], "Alice")
    XCTAssertEqual(map.lastEditSequence, 1)
  }

  func testReplayMergeEdit() throws {
    let editor = SpeakerEditor()
    let event = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .merge,
      speakerID: "speaker_1",
      targetID: "speaker_0",
      sequenceNumber: 1)

    let map = try editor.replay(
      events: [event],
      knownSpeakerIDs: knownSpeakerIDs,
      knownSegmentIDs: knownSegmentIDs,
      meetingID: meetingID)

    XCTAssertEqual(map.mergedSpeakers["speaker_0"], ["speaker_1"])
  }

  func testReplaySplitEdit() throws {
    let editor = SpeakerEditor()
    let segmentID = segmentIDs[0]
    let event = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .split,
      speakerID: "speaker_0",
      segmentID: segmentID,
      sequenceNumber: 1)

    let map = try editor.replay(
      events: [event],
      knownSpeakerIDs: knownSpeakerIDs,
      knownSegmentIDs: knownSegmentIDs,
      meetingID: meetingID)

    XCTAssertNotNil(map.splitSegments[segmentID])
    XCTAssertTrue(map.splitSegments[segmentID]!.hasPrefix("speaker_split_"))
  }

  func testReplayResetClearsAllState() throws {
    let editor = SpeakerEditor()
    let labelEvent = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .label,
      speakerID: "speaker_0",
      label: "Alice",
      sequenceNumber: 1)
    let mergeEvent = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .merge,
      speakerID: "speaker_1",
      targetID: "speaker_0",
      sequenceNumber: 2)
    let resetEvent = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .reset,
      sequenceNumber: 3)

    let map = try editor.replay(
      events: [labelEvent, mergeEvent, resetEvent],
      knownSpeakerIDs: knownSpeakerIDs,
      knownSegmentIDs: knownSegmentIDs,
      meetingID: meetingID)

    XCTAssertEqual(map.labels, [:])
    XCTAssertEqual(map.mergedSpeakers, [:])
    XCTAssertEqual(map.splitSegments, [:])
    XCTAssertEqual(map.lastEditSequence, 3)
  }

  func testReplayRejectsUnknownSpeakerID() throws {
    let editor = SpeakerEditor()
    let event = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .label,
      speakerID: "speaker_unknown",
      label: "Ghost",
      sequenceNumber: 1)

    XCTAssertThrowsError(
      try editor.replay(
        events: [event],
        knownSpeakerIDs: knownSpeakerIDs,
        knownSegmentIDs: knownSegmentIDs,
        meetingID: meetingID)
    ) { error in
      guard case SpeakerEditError.speakerNotFound = error else {
        return XCTFail("Expected speakerNotFound, got \(error)")
      }
    }
  }

  func testReplayRejectsMergeIntoSelf() throws {
    let editor = SpeakerEditor()
    let event = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .merge,
      speakerID: "speaker_0",
      targetID: "speaker_0",
      sequenceNumber: 1)

    XCTAssertThrowsError(
      try editor.replay(
        events: [event],
        knownSpeakerIDs: knownSpeakerIDs,
        knownSegmentIDs: knownSegmentIDs,
        meetingID: meetingID)
    ) { error in
      guard case SpeakerEditError.mergeIntoSelf = error else {
        return XCTFail("Expected mergeIntoSelf, got \(error)")
      }
    }
  }

  func testReplayRejectsEmptyLabel() throws {
    let editor = SpeakerEditor()
    let event = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .label,
      speakerID: "speaker_0",
      label: "",
      sequenceNumber: 1)

    XCTAssertThrowsError(
      try editor.replay(
        events: [event],
        knownSpeakerIDs: knownSpeakerIDs,
        knownSegmentIDs: knownSegmentIDs,
        meetingID: meetingID)
    ) { error in
      guard case SpeakerEditError.invalidLabel = error else {
        return XCTFail("Expected invalidLabel, got \(error)")
      }
    }
  }

  func testReplaySkipsDuplicateSequenceNumbers() throws {
    let editor = SpeakerEditor()
    let event1 = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .label,
      speakerID: "speaker_0",
      label: "Alice",
      sequenceNumber: 1)
    let event2 = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .label,
      speakerID: "speaker_0",
      label: "Bob",
      sequenceNumber: 1)

    let map = try editor.replay(
      events: [event1, event2],
      knownSpeakerIDs: knownSpeakerIDs,
      knownSegmentIDs: knownSegmentIDs,
      meetingID: meetingID)

    XCTAssertEqual(map.labels["speaker_0"], "Alice")
    XCTAssertEqual(map.lastEditSequence, 1)
  }

  // MARK: - Turn projection

  func testProjectTurnsReassignsMergedSpeakers() throws {
    let editor = SpeakerEditor()
    let map = SpeakerMap(
      meetingID: meetingID,
      mergedSpeakers: ["speaker_0": ["speaker_1"]])

    let turns = try [
      SpeakerTurn(speakerID: "speaker_0", startMS: 0, endMS: 1000),
      SpeakerTurn(speakerID: "speaker_1", startMS: 500, endMS: 1500),
      SpeakerTurn(speakerID: "speaker_2", startMS: 2000, endMS: 3000),
    ]

    let projected = try editor.projectTurns(turns, using: map)

    XCTAssertEqual(projected[0].speakerID, "speaker_0")
    XCTAssertEqual(projected[0].provenance, .engine)
    XCTAssertEqual(projected[1].speakerID, "speaker_0")
    XCTAssertEqual(projected[1].provenance, .userCorrected)
    XCTAssertEqual(projected[2].speakerID, "speaker_2")
    XCTAssertEqual(projected[2].provenance, .engine)
  }

  // MARK: - Alignment projection

  func testProjectAlignmentsAppliesMergeAndSplit() throws {
    let editor = SpeakerEditor()
    let segmentID = UUID()
    let map = SpeakerMap(
      meetingID: meetingID,
      mergedSpeakers: ["speaker_0": ["speaker_1"]],
      splitSegments: [segmentID: "speaker_new"])

    let alignment = try SpeakerAlignment(
      meetingID: meetingID,
      segmentID: segmentID,
      speakerID: "speaker_1",
      confidence: 0.95)

    let projected = try editor.projectAlignments([alignment], using: map)

    XCTAssertEqual(projected[0].speakerID, "speaker_new")
  }

  // MARK: - Event log round-trip

  func testSpeakerEditEventCodableRoundTrip() throws {
    let event = SpeakerEditEvent(
      eventID: UUID(),
      meetingID: meetingID,
      kind: .label,
      speakerID: "speaker_0",
      label: "テスト発言者",
      sequenceNumber: 1,
      occurredAt: Date())

    let data = try ContractCodec.encoder().encode(event)
    let decoded = try ContractCodec.decoder().decode(SpeakerEditEvent.self, from: data)

    XCTAssertEqual(decoded.eventID, event.eventID)
    XCTAssertEqual(decoded.kind, .label)
    XCTAssertEqual(decoded.label, "テスト発言者")
    XCTAssertEqual(decoded.sequenceNumber, 1)
  }

  func testSpeakerMapCodableRoundTrip() throws {
    let map = SpeakerMap(
      meetingID: meetingID,
      labels: ["speaker_0": "Alice", "speaker_1": "Böb"],
      mergedSpeakers: ["speaker_0": ["speaker_2"]],
      splitSegments: [UUID(): "speaker_new"],
      lastEditSequence: 5)

    let data = try ContractCodec.encoder().encode(map)
    let decoded = try ContractCodec.decoder().decode(SpeakerMap.self, from: data)

    XCTAssertEqual(decoded.meetingID, meetingID)
    XCTAssertEqual(decoded.labels["speaker_1"], "Böb")
    XCTAssertEqual(decoded.lastEditSequence, 5)
  }

  // MARK: - Merge with cascaded sources

  func testMergeFoldsSourceMergedSet() throws {
    let editor = SpeakerEditor()
    // First merge speaker_2 into speaker_1
    let merge1 = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .merge,
      speakerID: "speaker_2",
      targetID: "speaker_1",
      sequenceNumber: 1)
    // Then merge speaker_1 into speaker_0
    let merge2 = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .merge,
      speakerID: "speaker_1",
      targetID: "speaker_0",
      sequenceNumber: 2)

    let map = try editor.replay(
      events: [merge1, merge2],
      knownSpeakerIDs: knownSpeakerIDs,
      knownSegmentIDs: knownSegmentIDs,
      meetingID: meetingID)

    XCTAssertEqual(map.mergedSpeakers["speaker_0"], ["speaker_1", "speaker_2"])
  }

  // MARK: - Label transfer on merge

  func testLabelTransfersFromSourceToTarget() throws {
    let editor = SpeakerEditor()
    let labelEvent = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .label,
      speakerID: "speaker_1",
      label: "Bob",
      sequenceNumber: 1)
    let mergeEvent = SpeakerEditEvent(
      meetingID: meetingID,
      kind: .merge,
      speakerID: "speaker_1",
      targetID: "speaker_0",
      sequenceNumber: 2)

    let map = try editor.replay(
      events: [labelEvent, mergeEvent],
      knownSpeakerIDs: knownSpeakerIDs,
      knownSegmentIDs: knownSegmentIDs,
      meetingID: meetingID)

    XCTAssertEqual(map.labels["speaker_0"], "Bob")
    XCTAssertNil(map.labels["speaker_1"])
  }
}
