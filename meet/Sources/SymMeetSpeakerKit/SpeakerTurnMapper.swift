import Foundation
// @preconcurrency import documented: upstream SpeakerKit types predate
// strict concurrency; the import boundary contains all Sendable issues.
@preconcurrency import SpeakerKit
import SymMeetCore

/// Maps upstream ``SpeakerSegment`` values to meeting-local ``SpeakerTurn``s.
///
/// Stable speaker IDs are assigned per-meeting by sorting the upstream
/// integer speaker IDs deterministically and mapping each to a
/// `speaker_<index>` string.  The engine records the upstream-to-local
/// mapping so issue #17 can update it during speaker correction.
public struct SpeakerTurnMapper: Sendable {
  /// The upstream integer ID to meeting-local string ID mapping, ordered
  /// by upstream ID.  Exposed so issue #17 can remap after speaker merges.
  public let upstreamToLocal: [(upstream: Int, local: String)]

  public init(upstreamToLocal: [(upstream: Int, local: String)] = []) {
    self.upstreamToLocal = upstreamToLocal
  }

  /// Builds a mapper from the set of upstream speaker IDs observed in the
  /// diarization result.
  ///
  /// - Parameter upstreamIDs: All distinct upstream integer speaker IDs.
  /// - Returns: A mapper with deterministic `speaker_0`, `speaker_1`, ...
  ///   assignments sorted by upstream ID.
  public static func fromUpstreamIDs(_ upstreamIDs: Set<Int>) -> SpeakerTurnMapper {
    let sorted = upstreamIDs.sorted()
    let mapping = sorted.enumerated().map { (index, upstream) in
      (upstream: upstream, local: "speaker_\(index)")
    }
    return SpeakerTurnMapper(upstreamToLocal: mapping)
  }

  /// Maps upstream ``SpeakerSegment`` values to meeting-local ``SpeakerTurn``s.
  ///
  /// Segments whose upstream speaker ID is not in the mapping are assigned
  /// ``SpeakerAlignment/unknownSpeakerID`` with zero confidence.
  ///
  /// - Parameters:
  ///   - upstreamSegments: The segments from the upstream diarization result.
  ///   - meetingID: The meeting these turns belong to.
  /// - Returns: An ordered array of ``SpeakerTurn``s.
  public func mapToTurns(
    _ upstreamSegments: [SpeakerSegment],
    meetingID: UUID
  ) -> [SpeakerTurn] {
    let idMap = Dictionary(
      uniqueKeysWithValues: upstreamToLocal.map { ($0.upstream, $0.local) })

    return upstreamSegments.compactMap { segment in
      let speakerID: String
      let confidence: Double

      switch segment.speaker {
      case .speakerId(let id):
        speakerID = idMap[id] ?? SpeakerAlignment.unknownSpeakerID
        confidence = idMap[id] != nil ? 1.0 : 0
      case .noMatch, .multiple:
        speakerID = SpeakerAlignment.unknownSpeakerID
        confidence = 0
      @unknown default:
        speakerID = SpeakerAlignment.unknownSpeakerID
        confidence = 0
      }

      let startMS = Int((segment.startTime * 1_000).rounded())
      let endMS = Int((segment.endTime * 1_000).rounded())

      guard endMS > startMS else { return nil }

      return try? SpeakerTurn(
        speakerID: speakerID,
        startMS: startMS,
        endMS: endMS,
        confidence: confidence)
    }
  }
}
