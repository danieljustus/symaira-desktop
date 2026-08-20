import Foundation

/// The two subtitle formats symmeet renders. Timestamp separators are the
/// one hard format difference: SRT uses a comma (`00:00:00,000`), VTT a
/// period (`00:00:00.000`).
public enum SubtitleStyle: Sendable {
  case srt
  case vtt
}

/// Renders segments into SRT or VTT cues.
///
/// One ``Segment`` always becomes exactly one cue -- segments are never
/// merged or reordered, so overlapping speakers stay as separate,
/// explicitly-labeled cues rather than being collapsed into one (issue #11's
/// "never reorder speech to hide overlap" rule). Cues are emitted in
/// ``TranscriptRenderer/chronologicalOrder(_:_:)`` (by start time, then end
/// time, then speaker, then segment ID), which guarantees cue start times
/// are non-decreasing across the rendered stream -- the practically
/// achievable reading of "monotonic" for a format that must still support
/// overlapping cues (a later cue's *end* can legitimately fall before an
/// earlier, longer-running cue's end; only start order is a meaningful
/// invariant to hold across the whole stream).
public enum SubtitleRenderer {
  /// A conventional subtitle line budget. Text longer than this wraps onto
  /// additional lines within the same cue rather than being truncated or
  /// splitting mid-grapheme (important for CJK and RTL text, which may not
  /// contain the ASCII spaces this wrapper breaks on).
  private static let maxLineWidth = 42

  public static func render(segments: [Segment], style: SubtitleStyle) -> String {
    let ordered = segments.sorted(by: TranscriptRenderer.chronologicalOrder)

    guard !ordered.isEmpty else {
      return style == .vtt ? "WEBVTT\n" : ""
    }

    var blocks: [String] = []
    if style == .vtt { blocks.append("WEBVTT") }

    for (index, segment) in ordered.enumerated() {
      var block: [String] = []
      if style == .srt { block.append(String(index + 1)) }

      let separator: Character = style == .srt ? "," : "."
      let start = TimestampFormatter.format(segment.startMS, separator: separator)
      let end = TimestampFormatter.format(max(segment.endMS, segment.startMS), separator: separator)
      block.append("\(start) --> \(end)")

      let text = "\(segment.speakerID): \(segment.displayText)"
      block.append(contentsOf: wrap(text))

      blocks.append(block.joined(separator: "\n"))
    }

    return blocks.joined(separator: "\n\n") + "\n"
  }

  /// Word-wraps `text` to ``maxLineWidth``, splitting only on whitespace so
  /// multi-byte grapheme clusters (accented Latin, RTL scripts, combining
  /// marks) are never cut mid-character. A single "word" longer than the
  /// budget (common in CJK text, which has no spaces) is kept whole on its
  /// own line rather than being fractured.
  static func wrap(_ text: String, maxWidth: Int = maxLineWidth) -> [String] {
    guard !text.isEmpty else { return [""] }

    var lines: [String] = []
    var current = ""
    for word in text.split(separator: " ", omittingEmptySubsequences: false) {
      let candidate = current.isEmpty ? String(word) : "\(current) \(word)"
      if candidate.count > maxWidth, !current.isEmpty {
        lines.append(current)
        current = String(word)
      } else {
        current = candidate
      }
    }
    if !current.isEmpty || lines.isEmpty { lines.append(current) }
    return lines
  }
}
