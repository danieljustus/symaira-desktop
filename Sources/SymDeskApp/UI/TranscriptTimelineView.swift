import SwiftUI
import SymairaTheme

/// Renders a meeting's reviewed transcript.
///
/// Named to match the eventual segment-synced timeline described in #172,
/// but today SymMeet's export only returns rendered Markdown text
/// (`symmeet export ... --format markdown`), not structured per-segment
/// timestamps — so this view has no segment data to seek against yet.
/// Time-synced playback needs a structured `symmeet transcript show --json`
/// (or equivalent) contract as a prerequisite; see the #172 follow-up note.
struct TranscriptTimelineView: View {
    let transcript: String?

    var body: some View {
        ScrollView {
            if let transcript, !transcript.isEmpty {
                Text(transcript)
                    .font(.system(.body, design: .monospaced))
                    .foregroundColor(SymairaTheme.textPrimary)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(16)
            } else {
                VStack(spacing: 8) {
                    Image(systemName: "text.badge.xmark")
                        .font(.system(size: 32))
                        .foregroundColor(SymairaTheme.textMuted)
                    Text("Transcript unavailable")
                        .font(.callout)
                        .foregroundColor(SymairaTheme.textMuted)
                }
                .frame(maxWidth: .infinity, minHeight: 160)
            }
        }
    }
}
