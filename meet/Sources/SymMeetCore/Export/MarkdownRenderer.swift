import Foundation

/// Renders a meeting's segments into the v1 ``MarkdownProjection`` contract.
/// This does not invent a new Markdown shape: every export goes through
/// `MarkdownProjection`'s own initializer, so the stable `## Summary` /
/// `## Decisions` / `## Action Items` / `## Participants` / `## Transcript`
/// heading structure is guaranteed to match every other consumer of that
/// contract (see `Sources/SymMeetCore/Contracts/MarkdownProjection.swift`).
///
/// Summarization, decisions, and action items are out of scope for #11 (no
/// AI-generated content) -- those sections are always empty here. Only
/// participants (derived directly from segment speaker IDs) and the
/// transcript itself are populated from real meeting data.
public enum MarkdownRenderer {
  public static func render(manifest: MeetingManifest, segments: [Segment]) -> String {
    let ordered = segments.sorted(by: TranscriptRenderer.chronologicalOrder)
    let participants = Set(segments.map(\.speakerID)).sorted()
    let transcript = ordered.map(transcriptEntry).joined(separator: "\n\n")

    let projection = MarkdownProjection(
      summary: "",
      decisions: [],
      actionItems: [],
      participants: participants,
      transcript: transcript)
    return projection.markdown
  }

  private static func transcriptEntry(_ segment: Segment) -> String {
    let start = TimestampFormatter.format(segment.startMS, separator: ".")
    let end = TimestampFormatter.format(segment.endMS, separator: ".")
    return "**\(segment.speakerID)** [\(start) - \(end)]\n\n\(segment.displayText)"
  }
}
