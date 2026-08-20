import Foundation

/// Applies speaker correction edits deterministically and supports
/// event-log replay.
///
/// The editor never mutates raw diarization output.  All corrections are
/// projected onto derived artifacts (the ``SpeakerMap`` and edited turns).
/// Re-running diarization does not silently apply stale edits to new speaker
/// IDs because the edit event log is keyed by speaker ID, which changes
/// whenever diarization is re-run.
public struct SpeakerEditor: Sendable {
  /// Maximum allowed display label length in characters.
  public static let maxLabelLength = 128

  public init() {}

  // MARK: - Event replay

  /// Replays an ordered sequence of edit events to reconstruct a
  /// ``SpeakerMap``.  The caller is responsible for passing events in
  /// sequence-number order.  Events with a sequence number less than or
  /// equal to the map's `lastEditSequence` are skipped (idempotent replay).
  ///
  /// A `.reset` event clears all prior state and starts fresh.
  ///
  /// - Parameters:
  ///   - events: Edit events in sequence-number order.
  ///   - knownSpeakerIDs: The set of speaker IDs from the latest raw
  ///     diarization output.  Used to reject edits referencing unknown IDs.
  ///   - knownSegmentIDs: The set of segment IDs from the current
  ///     transcript.  Used to reject split edits referencing unknown segments.
  ///   - meetingID: The meeting these events belong to.
  /// - Returns: The reconstructed speaker map.
  public func replay(
    events: [SpeakerEditEvent],
    knownSpeakerIDs: Set<String>,
    knownSegmentIDs: Set<UUID>,
    meetingID: UUID
  ) throws -> SpeakerMap {
    var labels: [String: String] = [:]
    var mergedSpeakers: [String: [String]] = [:]
    var splitSegments: [UUID: String] = [:]
    var lastSequence = 0

    for event in events {
      guard event.sequenceNumber > lastSequence else { continue }

      switch event.kind {
      case .label:
        guard let speakerID = event.speakerID, let labelText = event.label else {
          throw SpeakerEditError.corruptEventLog
        }
        guard !labelText.isEmpty, labelText.count <= Self.maxLabelLength else {
          throw SpeakerEditError.invalidLabel(labelText)
        }
        guard knownSpeakerIDs.contains(speakerID) else {
          throw SpeakerEditError.speakerNotFound(speakerID)
        }
        labels[speakerID] = labelText

      case .merge:
        guard let fromID = event.speakerID, let intoID = event.targetID else {
          throw SpeakerEditError.corruptEventLog
        }
        guard fromID != intoID else {
          throw SpeakerEditError.mergeIntoSelf(fromID)
        }
        guard knownSpeakerIDs.contains(fromID) else {
          throw SpeakerEditError.speakerNotFound(fromID)
        }
        guard knownSpeakerIDs.contains(intoID) else {
          throw SpeakerEditError.speakerNotFound(intoID)
        }
        // Check for circular merge: intoID must not already be merged into fromID.
        if let existing = mergedSpeakers[fromID], existing.contains(intoID) {
          throw SpeakerEditError.circularMerge(from: fromID, to: intoID)
        }
        // Move the source into the target's merged set.
        var targetMerged = mergedSpeakers[intoID] ?? []
        targetMerged.append(fromID)
        mergedSpeakers[intoID] = targetMerged
        // If the source had its own merged set, fold it into the target.
        if let sourceMerged = mergedSpeakers.removeValue(forKey: fromID) {
          targetMerged.append(contentsOf: sourceMerged)
          mergedSpeakers[intoID] = targetMerged
        }
        // Transfer any label from source to target.
        if let sourceLabel = labels.removeValue(forKey: fromID) {
          labels[intoID] = sourceLabel
        }

      case .split:
        guard let speakerID = event.speakerID, let segID = event.segmentID else {
          throw SpeakerEditError.corruptEventLog
        }
        guard knownSpeakerIDs.contains(speakerID) else {
          throw SpeakerEditError.speakerNotFound(speakerID)
        }
        guard knownSegmentIDs.contains(segID) else {
          throw SpeakerEditError.segmentNotFound(segID)
        }
        // Generate a new unique speaker ID for the split segment.
        let newSpeakerID = "speaker_split_\(segID.uuidString.prefix(8))"
        splitSegments[segID] = newSpeakerID

      case .reset:
        labels.removeAll()
        mergedSpeakers.removeAll()
        splitSegments.removeAll()
      }

      lastSequence = event.sequenceNumber
    }

    return SpeakerMap(
      meetingID: meetingID,
      labels: labels,
      mergedSpeakers: mergedSpeakers,
      splitSegments: splitSegments,
      lastEditSequence: lastSequence)
  }

  // MARK: - Turn projection

  /// Projects raw speaker turns through the speaker map to produce
  /// edited turns.  Merged speakers are reassigned to their canonical
  /// target.  Split segments get their new speaker ID.
  ///
  /// Raw turns are never modified -- the returned array contains new
  /// ``SpeakerTurn`` values with ``TurnProvenance/userCorrected`` provenance
  /// when the speaker ID changed.
  ///
  /// - Parameters:
  ///   - turns: The raw diarization turns.
  ///   - map: The current speaker map.
  /// - Returns: An array of turns with speaker IDs projected through the map.
  public func projectTurns(
    _ turns: [SpeakerTurn],
    using map: SpeakerMap
  ) throws -> [SpeakerTurn] {
    // Build a reverse lookup: for any speaker that was merged, find its
    // canonical target.
    var mergeTarget: [String: String] = [:]
    for (target, sources) in map.mergedSpeakers {
      for source in sources {
        mergeTarget[source] = target
      }
    }

    return try turns.map { turn in
      let resolvedID = mergeTarget[turn.speakerID] ?? turn.speakerID
      let provenance: TurnProvenance =
        resolvedID != turn.speakerID ? .userCorrected : turn.provenance
      if provenance == .userCorrected {
        return try SpeakerTurn(
          turnID: turn.turnID,
          speakerID: resolvedID,
          startMS: turn.startMS,
          endMS: turn.endMS,
          confidence: turn.confidence,
          isOverlapping: turn.isOverlapping,
          provenance: provenance,
          additionalFields: turn.additionalFields)
      }
      return turn
    }
  }

  // MARK: - Alignment projection

  /// Projects speaker alignments through the speaker map.  Applies both
  /// merge resolution and split overrides.
  ///
  /// - Parameters:
  ///   - alignments: The raw speaker alignments.
  ///   - map: The current speaker map.
  /// - Returns: An array of alignments with speaker IDs projected through
  ///   the map.
  public func projectAlignments(
    _ alignments: [SpeakerAlignment],
    using map: SpeakerMap
  ) throws -> [SpeakerAlignment] {
    // Build a reverse lookup for merges.
    var mergeTarget: [String: String] = [:]
    for (target, sources) in map.mergedSpeakers {
      for source in sources {
        mergeTarget[source] = target
      }
    }

    return try alignments.map { alignment in
      var resolvedID = mergeTarget[alignment.speakerID] ?? alignment.speakerID
      // Apply split override if this segment was split.
      if let splitID = map.splitSegments[alignment.segmentID] {
        resolvedID = splitID
      }
      let changed = resolvedID != alignment.speakerID
      return try SpeakerAlignment(
        meetingID: alignment.meetingID,
        segmentID: alignment.segmentID,
        speakerID: resolvedID,
        confidence: changed ? min(alignment.confidence, 0.9) : alignment.confidence,
        overlappingSpeakers: alignment.overlappingSpeakers)
    }
  }

  // MARK: - Validation

  /// Validates a label edit before it is appended to the event log.
  public func validateLabel(_ label: String) throws {
    guard !label.isEmpty else {
      throw SpeakerEditError.invalidLabel(label)
    }
    guard label.count <= Self.maxLabelLength else {
      throw SpeakerEditError.invalidLabel(label)
    }
  }

  /// Validates a merge edit before it is appended to the event log.
  public func validateMerge(
    from sourceID: String,
    to targetID: String,
    knownSpeakerIDs: Set<String>
  ) throws {
    guard sourceID != targetID else {
      throw SpeakerEditError.mergeIntoSelf(sourceID)
    }
    guard knownSpeakerIDs.contains(sourceID) else {
      throw SpeakerEditError.speakerNotFound(sourceID)
    }
    guard knownSpeakerIDs.contains(targetID) else {
      throw SpeakerEditError.speakerNotFound(targetID)
    }
  }

  /// Validates a split edit before it is appended to the event log.
  public func validateSplit(
    speakerID: String,
    segmentID: UUID,
    knownSpeakerIDs: Set<String>,
    knownSegmentIDs: Set<UUID>
  ) throws {
    guard knownSpeakerIDs.contains(speakerID) else {
      throw SpeakerEditError.speakerNotFound(speakerID)
    }
    guard knownSegmentIDs.contains(segmentID) else {
      throw SpeakerEditError.segmentNotFound(segmentID)
    }
  }
}
