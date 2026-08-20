import Foundation

/// Deduplicates and converts engine ``SegmentDraft`` output into durable
/// ``Segment`` records as they are finalized.
///
/// Engines mint a fresh random ``SegmentDraft/segmentID`` on every attempt,
/// so identity cannot be tracked by that field across a retry. Instead this
/// tracks identity by `(trackID, startMS, endMS)`: the span of source audio a
/// segment covers is stable across attempts even when the segment's random
/// identifier is not. This is what lets a retry that reprocesses audio whose
/// time range was already finalized (for example because a crash landed
/// between writing a segment to disk and recording the checkpoint that
/// would have skipped it) avoid persisting a duplicate line to
/// `segments.raw.jsonl`.
public struct SegmentAccumulator: Sendable {
  private struct Key: Hashable, Sendable {
    let trackID: UUID
    let startMS: Int
    let endMS: Int
  }

  private var seen: Set<Key>

  /// - Parameter existing: Segments already finalized and persisted in a
  ///   prior attempt, read back from disk before a retry resumes so their
  ///   time ranges are never finalized twice.
  public init(existing: [Segment] = []) {
    seen = Set(existing.map { Key(trackID: $0.trackID, startMS: $0.startMS, endMS: $0.endMS) })
  }

  /// Records one finalized draft, returning the durable ``Segment`` to
  /// persist, or `nil` if a segment covering the exact same track and time
  /// range has already been finalized (a duplicate that must not be
  /// persisted again).
  public mutating func finalize(_ draft: SegmentDraft) throws -> Segment? {
    let key = Key(trackID: draft.trackID, startMS: draft.startMS, endMS: draft.endMS)
    guard !seen.contains(key) else { return nil }
    seen.insert(key)
    return try Segment(
      segmentID: draft.segmentID,
      trackID: draft.trackID,
      speakerID: draft.speakerID,
      startMS: draft.startMS,
      endMS: draft.endMS,
      engineText: draft.text)
  }
}
