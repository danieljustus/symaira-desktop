import Foundation
// @preconcurrency import documented: upstream SpeakerKit types predate
// strict concurrency; the import boundary contains all Sendable issues.
@preconcurrency import SpeakerKit
import SymMeetCore

/// Handles the track-aware diarization rules from issue #16:
///
/// - **Imported mixed recordings**: diarize the single normalized mixed source.
/// - **Native dual-track recordings**: the microphone track is mapped to
///   ``LocalSpeaker/reservedID`` (never clustered), and the system track is
///   diarized for remote speakers.
///
/// All processing is local -- no network calls, no participant names.
public struct TrackAwareDiarizer: Sendable {
  public init() {}

  /// Processes upstream diarization segments respecting the track model.
  ///
  /// For imported mixed recordings, all upstream segments are mapped to
  /// meeting-local speaker IDs.  For native dual-track recordings, the
  /// microphone track is inserted as a reserved local speaker turn and the
  /// upstream system-track segments are mapped to remote speaker IDs.
  ///
  /// - Parameters:
  ///   - request: The diarization request carrying track info and duration.
  ///   - upstreamSegments: The raw ``SpeakerSegment``s from the upstream engine.
  /// - Returns: A ``ProcessedDiarization`` with mapped turns and RTTM lines.
  public func process(
    _ request: DiarizationRequest,
    upstreamSegments: [SpeakerSegment]
  ) -> ProcessedDiarization {
    switch request.sourceKind {
    case .importedMixed:
      return processImportedMixed(request, upstreamSegments: upstreamSegments)
    case .nativeDualTrack:
      return processNativeDualTrack(request, upstreamSegments: upstreamSegments)
    }
  }

  // MARK: - Imported mixed

  private func processImportedMixed(
    _ request: DiarizationRequest,
    upstreamSegments: [SpeakerSegment]
  ) -> ProcessedDiarization {
    let allUpstreamIDs = Set(upstreamSegments.compactMap { $0.speaker.speakerId })
    let mapper = SpeakerTurnMapper.fromUpstreamIDs(allUpstreamIDs)
    let turns = mapper.mapToTurns(upstreamSegments, meetingID: request.meetingID)
    let rttm = buildRTTM(turns: turns, meetingID: request.meetingID)

    return ProcessedDiarization(
      turns: turns,
      speakerCount: mapper.upstreamToLocal.count,
      rttmLines: rttm)
  }

  // MARK: - Native dual track

  private func processNativeDualTrack(
    _ request: DiarizationRequest,
    upstreamSegments: [SpeakerSegment]
  ) -> ProcessedDiarization {
    let remoteUpstreamIDs = Set(upstreamSegments.compactMap { $0.speaker.speakerId })

    // Build the mapper: reserve speaker_local for the local microphone and
    // assign remote speakers starting from speaker_1.
    let sortedRemote = remoteUpstreamIDs.sorted()
    var mapping: [(upstream: Int, local: String)] = [
      (upstream: -1, local: LocalSpeaker.reservedID)
    ]
    for (index, upstreamID) in sortedRemote.enumerated() {
      mapping.append((upstream: upstreamID, local: "speaker_\(index + 1)"))
    }
    let mapper = SpeakerTurnMapper(upstreamToLocal: mapping)

    var turns = mapper.mapToTurns(upstreamSegments, meetingID: request.meetingID)

    // If a microphone track was provided, map it to the reserved local
    // speaker covering the full duration.
    if let micSamples = request.microphoneSamples, !micSamples.isEmpty {
      let micDurationMS = request.durationMS
      if let localTurn = try? SpeakerTurn(
        speakerID: LocalSpeaker.reservedID,
        startMS: 0,
        endMS: micDurationMS
      ) {
        turns.insert(localTurn, at: 0)
      }
    }

    let rttm = buildRTTM(turns: turns, meetingID: request.meetingID)
    return ProcessedDiarization(
      turns: turns,
      speakerCount: mapper.upstreamToLocal.count,
      rttmLines: rttm)
  }

  // MARK: - RTTM

  private func buildRTTM(turns: [SpeakerTurn], meetingID: UUID) -> [String] {
    let fileID = meetingID.uuidString.lowercased()
    return turns.map { turn in
      let startSeconds = Float(turn.startMS) / 1_000
      let durationSeconds = Float(turn.endMS - turn.startMS) / 1_000
      let startStr = String(format: "%.3f", startSeconds)
      let durStr = String(format: "%.3f", durationSeconds)
      return "SPEAKER \(fileID) 1 \(startStr) \(durStr)"
        + " <NA> <NA> \(turn.speakerID) <NA> <NA>"
    }
  }
}

/// Intermediate result after track-aware processing, before final assembly
/// into ``DiarizationOutput``.
public struct ProcessedDiarization: Equatable, Sendable {
  public let turns: [SpeakerTurn]
  public let speakerCount: Int
  public let rttmLines: [String]

  public init(turns: [SpeakerTurn], speakerCount: Int, rttmLines: [String]) {
    self.turns = turns
    self.speakerCount = speakerCount
    self.rttmLines = rttmLines
  }
}
