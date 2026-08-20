import Foundation

public struct MarkdownProjection: Equatable, Sendable {
  public let markdown: String

  public init(
    summary: String,
    decisions: [String],
    actionItems: [String],
    participants: [String],
    transcript: String
  ) {
    func list(_ values: [String]) -> String {
      values.isEmpty ? "- None" : values.map { "- \($0)" }.joined(separator: "\n")
    }

    markdown = """
      ## Summary

      \(summary)

      ## Decisions

      \(list(decisions))

      ## Action Items

      \(list(actionItems))

      ## Participants

      \(list(participants))

      ## Transcript

      \(transcript)
      """
  }
}
