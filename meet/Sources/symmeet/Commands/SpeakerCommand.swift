import ArgumentParser
import Foundation
import SymMeetCore

extension SymMeet {
  struct Speaker: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "speaker",
      abstract: "Manage speaker labels, merges, splits, and resets.",
      subcommands: [
        SpeakerList.self, SpeakerLabel.self, SpeakerMerge.self,
        SpeakerSplit.self, SpeakerReset.self,
      ]
    )
  }

  struct SpeakerList: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "list",
      abstract: "List speakers and their labels for a meeting."
    )

    @Argument(help: "The meeting UUID.")
    var meetingID: String

    @Flag(name: .long, help: "Emit one machine-readable result document.")
    var json = false

    mutating func run() async throws {
      do {
        let store = MeetingStore()
        let normalizedID = try await resolveNormalizedID(store: store, meetingID: meetingID)
        let edits = try await store.speakerEdits(meetingID: normalizedID)
        let turns = try await store.rawTurns(meetingID: normalizedID)
        let knownSpeakerIDs = Set(turns.map(\.speakerID))

        let editor = SpeakerEditor()
        let meetingUUID = UUID(uuidString: normalizedID) ?? UUID()
        let map = try editor.replay(
          events: edits,
          knownSpeakerIDs: knownSpeakerIDs,
          knownSegmentIDs: [],
          meetingID: meetingUUID)

        if json {
          try Output.writeJSON(
            SpeakerListOutput(
              meetingID: normalizedID,
              speakers: knownSpeakerIDs.sorted(),
              labels: map.labels,
              mergedSpeakers: map.mergedSpeakers))
        } else if knownSpeakerIDs.isEmpty {
          Output.writeLine("No speakers found.")
        } else {
          for speakerID in knownSpeakerIDs.sorted() {
            let label = map.labels[speakerID] ?? speakerID
            let merged = map.mergedSpeakers[speakerID]
            var line = "\(speakerID)\t\(label)"
            if let merged, !merged.isEmpty {
              line += "\tmerged: \(merged.joined(separator: ", "))"
            }
            Output.writeLine(line)
          }
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  struct SpeakerLabel: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "label",
      abstract: "Assign a display label to an anonymous speaker."
    )

    @Argument(help: "The meeting UUID.")
    var meetingID: String

    @Argument(help: "The anonymous speaker ID (e.g. 'speaker_0').")
    var speakerID: String

    @Argument(help: "The display label to assign.")
    var label: String

    @Flag(name: .long, help: "Emit one machine-readable result document.")
    var json = false

    mutating func run() async throws {
      do {
        let store = MeetingStore()
        let normalizedID = try await resolveNormalizedID(store: store, meetingID: meetingID)
        let meetingUUID = UUID(uuidString: normalizedID) ?? UUID()

        let turns = try await store.rawTurns(meetingID: normalizedID)
        let knownSpeakerIDs = Set(turns.map(\.speakerID))
        guard knownSpeakerIDs.contains(speakerID) else {
          throw SpeakerEditError.speakerNotFound(speakerID)
        }

        let editor = SpeakerEditor()
        try editor.validateLabel(label)

        let edits = try await store.speakerEdits(meetingID: normalizedID)
        let nextSequence = (edits.map(\.sequenceNumber).max() ?? 0) + 1
        let event = SpeakerEditEvent(
          meetingID: meetingUUID,
          kind: .label,
          speakerID: speakerID,
          label: label,
          sequenceNumber: nextSequence)
        try await store.appendSpeakerEdit(event, meetingID: normalizedID)

        if json {
          try Output.writeJSON(
            SpeakerMutationOutput(
              meetingID: normalizedID,
              speakerID: speakerID,
              label: label,
              status: "labeled"))
        } else {
          Output.writeLine("Labeled \(speakerID) as '\(label)'.")
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  struct SpeakerMerge: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "merge",
      abstract: "Merge one speaker into another."
    )

    @Argument(help: "The meeting UUID.")
    var meetingID: String

    @Argument(help: "The source speaker ID to merge from.")
    var fromID: String

    @Argument(help: "The target speaker ID to merge into.")
    var intoID: String

    @Flag(name: .long, help: "Emit one machine-readable result document.")
    var json = false

    mutating func run() async throws {
      do {
        let store = MeetingStore()
        let normalizedID = try await resolveNormalizedID(store: store, meetingID: meetingID)
        let meetingUUID = UUID(uuidString: normalizedID) ?? UUID()

        let turns = try await store.rawTurns(meetingID: normalizedID)
        let knownSpeakerIDs = Set(turns.map(\.speakerID))

        let editor = SpeakerEditor()
        try editor.validateMerge(
          from: fromID, to: intoID, knownSpeakerIDs: knownSpeakerIDs)

        let edits = try await store.speakerEdits(meetingID: normalizedID)
        let nextSequence = (edits.map(\.sequenceNumber).max() ?? 0) + 1
        let event = SpeakerEditEvent(
          meetingID: meetingUUID,
          kind: .merge,
          speakerID: fromID,
          targetID: intoID,
          sequenceNumber: nextSequence)
        try await store.appendSpeakerEdit(event, meetingID: normalizedID)

        if json {
          try Output.writeJSON(
            SpeakerMergeOutput(
              meetingID: normalizedID,
              fromID: fromID,
              intoID: intoID,
              status: "merged"))
        } else {
          Output.writeLine("Merged \(fromID) into \(intoID).")
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  struct SpeakerSplit: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "split",
      abstract: "Split a segment away from its current speaker."
    )

    @Argument(help: "The meeting UUID.")
    var meetingID: String

    @Argument(help: "The speaker ID to split from.")
    var speakerID: String

    @Option(name: .long, help: "The segment UUID to split out.")
    var segment: String

    @Flag(name: .long, help: "Emit one machine-readable result document.")
    var json = false

    mutating func run() async throws {
      do {
        let store = MeetingStore()
        let normalizedID = try await resolveNormalizedID(store: store, meetingID: meetingID)
        let meetingUUID = UUID(uuidString: normalizedID) ?? UUID()

        guard let segmentUUID = UUID(uuidString: segment) else {
          throw CLIError(
            exitCode: CLIExit.usage.rawValue,
            code: "speaker_not_found",
            message: "Invalid segment UUID: '\(segment)'.",
            isJSON: json)
        }

        let turns = try await store.rawTurns(meetingID: normalizedID)
        let knownSpeakerIDs = Set(turns.map(\.speakerID))
        let segments = try await store.rawSegments(meetingID: normalizedID)
        let knownSegmentIDs = Set(segments.map(\.segmentID))

        let editor = SpeakerEditor()
        try editor.validateSplit(
          speakerID: speakerID,
          segmentID: segmentUUID,
          knownSpeakerIDs: knownSpeakerIDs,
          knownSegmentIDs: knownSegmentIDs)

        let edits = try await store.speakerEdits(meetingID: normalizedID)
        let nextSequence = (edits.map(\.sequenceNumber).max() ?? 0) + 1
        let event = SpeakerEditEvent(
          meetingID: meetingUUID,
          kind: .split,
          speakerID: speakerID,
          segmentID: segmentUUID,
          sequenceNumber: nextSequence)
        try await store.appendSpeakerEdit(event, meetingID: normalizedID)

        if json {
          try Output.writeJSON(
            SpeakerSplitOutput(
              meetingID: normalizedID,
              speakerID: speakerID,
              segmentID: segment,
              status: "split"))
        } else {
          Output.writeLine("Split segment \(segment) from \(speakerID).")
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  struct SpeakerReset: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "reset",
      abstract: "Reset all speaker edits for a meeting."
    )

    @Argument(help: "The meeting UUID.")
    var meetingID: String

    @Flag(name: .long, help: "Emit one machine-readable result document.")
    var json = false

    mutating func run() async throws {
      do {
        let store = MeetingStore()
        let normalizedID = try await resolveNormalizedID(store: store, meetingID: meetingID)
        let meetingUUID = UUID(uuidString: normalizedID) ?? UUID()

        let edits = try await store.speakerEdits(meetingID: normalizedID)
        let nextSequence = (edits.map(\.sequenceNumber).max() ?? 0) + 1
        let event = SpeakerEditEvent(
          meetingID: meetingUUID,
          kind: .reset,
          sequenceNumber: nextSequence)
        try await store.appendSpeakerEdit(event, meetingID: normalizedID)

        // Clear the derived speaker map.
        let emptyMap = SpeakerMap(meetingID: meetingUUID)
        try await store.writeSpeakerMap(emptyMap, meetingID: normalizedID)

        if json {
          try Output.writeJSON(
            SpeakerMutationOutput(
              meetingID: normalizedID,
              speakerID: nil,
              label: nil,
              status: "reset"))
        } else {
          Output.writeLine("Reset all speaker edits for \(normalizedID).")
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }
}

// MARK: - Helpers

private func resolveNormalizedID(store: MeetingStore, meetingID: String) async throws -> String {
  let manifest = try await store.load(meetingID: meetingID)
  return manifest.meetingID.uuidString.lowercased()
}

// MARK: - Output types

private struct SpeakerListOutput: Encodable {
  let meetingID: String
  let speakers: [String]
  let labels: [String: String]
  let mergedSpeakers: [String: [String]]
}

private struct SpeakerMutationOutput: Encodable {
  let meetingID: String
  let speakerID: String?
  let label: String?
  let status: String
}

private struct SpeakerMergeOutput: Encodable {
  let meetingID: String
  let fromID: String
  let intoID: String
  let status: String
}

private struct SpeakerSplitOutput: Encodable {
  let meetingID: String
  let speakerID: String
  let segmentID: String
  let status: String
}
