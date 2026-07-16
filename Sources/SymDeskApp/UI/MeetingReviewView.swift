import SwiftUI
import SymDeskCore
import SymairaTheme

/// Detail/review pane for the currently selected meeting: metadata,
/// participants, audio transport (when available), and the transcript.
struct MeetingReviewView: View {
    @ObservedObject var model: MeetingReviewModel
    @StateObject private var audioPlayer = MeetingAudioPlayerModel()

    var body: some View {
        Group {
            switch model.detailState {
            case .idle, .loading:
                ProgressView("Loading meeting…")
                    .tint(SymairaTheme.goldPrimary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            case .failed(let message):
                failedState(message)
            case .loaded:
                if let detail = model.selectedDetail {
                    loadedContent(detail)
                } else {
                    // Detail state can briefly read .loaded before selectedDetail
                    // is published on the same run loop tick; treat as loading
                    // rather than showing an empty pane.
                    ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
        }
    }

    private func failedState(_ message: String) -> some View {
        VStack(spacing: 12) {
            Image(systemName: "exclamationmark.triangle")
                .font(.system(size: 40))
                .foregroundColor(SymairaTheme.goldPrimary)
            Text("Couldn't load this meeting")
                .font(.title3)
                .foregroundColor(SymairaTheme.textPrimary)
            Text(message)
                .font(.caption)
                .foregroundColor(SymairaTheme.textSecondary)
                .multilineTextAlignment(.center)
            if let path = model.selectedPath {
                Button("Retry") {
                    Task { await model.selectMeeting(path: path) }
                }
                .buttonStyle(SymairaSecondaryButtonStyle())
            }
        }
        .frame(maxWidth: 420)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func loadedContent(_ detail: MeetingDetail) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            metadataHeader(detail)
            Divider()

            HStack(alignment: .top, spacing: 0) {
                ScrollView {
                    VStack(alignment: .leading, spacing: 16) {
                        SpeakerReviewPanel(participants: detail.frontmatter.participants ?? [])
                        MeetingAudioPlayerView(model: audioPlayer, hasSource: false)
                    }
                    .padding(16)
                }
                .frame(minWidth: 260, maxWidth: 320)

                Divider()

                TranscriptTimelineView(transcript: model.transcript)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }

            if let refreshError = model.refreshError {
                Text(refreshError)
                    .font(.caption)
                    .foregroundColor(.red)
                    .padding(8)
            }
        }
    }

    private func metadataHeader(_ detail: MeetingDetail) -> some View {
        HStack(alignment: .firstTextBaseline) {
            VStack(alignment: .leading, spacing: 4) {
                Text(detail.title.isEmpty ? detail.frontmatter.meetingID : detail.title)
                    .font(.title3)
                    .fontWeight(.bold)
                    .foregroundColor(SymairaTheme.textPrimary)
                HStack(spacing: 10) {
                    Label(detail.frontmatter.startedAt, systemImage: "calendar")
                    if let language = detail.frontmatter.language, !language.isEmpty {
                        Label(language, systemImage: "globe")
                    }
                    if let ms = detail.frontmatter.durationMS, ms > 0 {
                        Label(Self.formatDuration(ms), systemImage: "clock")
                    }
                }
                .font(.caption)
                .foregroundColor(SymairaTheme.textSecondary)
            }
            Spacer()
            if model.isRefreshing {
                ProgressView().controlSize(.small)
            } else {
                Button("Refresh Transcript") {
                    Task { await model.refreshSelected(apply: true) }
                }
                .buttonStyle(SymairaSecondaryButtonStyle())
                .controlSize(.small)
            }
        }
        .padding(16)
    }

    private static func formatDuration(_ ms: Int64) -> String {
        let totalSeconds = Int(ms / 1000)
        let minutes = totalSeconds / 60
        let seconds = totalSeconds % 60
        return String(format: "%d:%02d", minutes, seconds)
    }
}
