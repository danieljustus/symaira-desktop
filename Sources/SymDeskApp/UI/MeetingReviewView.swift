import SwiftUI
import SymDeskCore
import SymairaTheme

/// Detail/review pane for the currently selected meeting: metadata,
/// participants, audio transport (when available), and the transcript.
struct MeetingReviewView: View {
    @ObservedObject var model: MeetingReviewModel
    @StateObject private var audioPlayer = MeetingAudioPlayerModel()
    @State private var showPublishSheet = false

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
                .symairaText(.title)
                .foregroundColor(SymairaTheme.goldPrimary)
            Text("Couldn't load this meeting")
                .symairaText(.heading)
                .foregroundColor(SymairaTheme.textPrimary)
            Text(message)
                .symairaText(.caption)
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
                        SpeakerReviewPanel(model: model)
                        MeetingAudioPlayerView(model: audioPlayer, hasSource: false)
                    }
                    .padding(16)
                }
                .frame(minWidth: 260, maxWidth: 320)

                Divider()

                TranscriptTimelineView(
                    transcript: model.transcript,
                    segments: model.segments,
                    segmentsError: model.segmentsError,
                    selectedSegmentID: model.selectedSegmentID,
                    currentPlaybackMS: audioPlayer.isPlaying ? Int64(audioPlayer.currentTime * 1000) : nil,
                    speakerLabels: Dictionary(uniqueKeysWithValues: model.speakers.map { ($0.speakerID, $0.label) }),
                    onSelectSegment: { selectSegment($0) }
                )
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }

            ForEach([model.refreshError, model.reviewSaveError].compactMap { $0 }, id: \.self) { error in
                Text(error)
                    .symairaText(.caption)
                    .foregroundColor(.red)
                    .padding(8)
            }
        }
        .background(keyboardShortcuts)
        .sheet(isPresented: $showPublishSheet) {
            MeetingKnowledgeReviewView(model: model)
        }
    }

    /// Selecting a segment highlights it and seeks playback to its start —
    /// and the reverse sync (playback position highlighting the row) comes
    /// from `currentPlaybackMS` above, driven by the player's 50 ms time
    /// observer, which keeps highlight and audio within the 100 ms budget.
    private func selectSegment(_ segment: MeetingSegment) {
        model.selectedSegmentID = segment.segmentID
        audioPlayer.seek(to: TimeInterval(segment.startMS) / 1000)
    }

    /// Hidden keyboard controls for review: J/K step segments, ←/→ jump
    /// playback by 5 s. Play/pause on space lives on the player's own
    /// transport button.
    private var keyboardShortcuts: some View {
        Group {
            Button("Next segment") {
                if let segment = model.stepSegment(1) {
                    audioPlayer.seek(to: TimeInterval(segment.startMS) / 1000)
                }
            }
            .keyboardShortcut("j", modifiers: [])
            Button("Previous segment") {
                if let segment = model.stepSegment(-1) {
                    audioPlayer.seek(to: TimeInterval(segment.startMS) / 1000)
                }
            }
            .keyboardShortcut("k", modifiers: [])
            Button("Jump back 5 seconds") {
                audioPlayer.seek(to: max(audioPlayer.currentTime - 5, 0))
            }
            .keyboardShortcut(.leftArrow, modifiers: [])
            Button("Jump forward 5 seconds") {
                audioPlayer.seek(to: audioPlayer.currentTime + 5)
            }
            .keyboardShortcut(.rightArrow, modifiers: [])
        }
        .opacity(0)
        .frame(width: 0, height: 0)
        .accessibilityHidden(true)
    }

    private func metadataHeader(_ detail: MeetingDetail) -> some View {
        HStack(alignment: .firstTextBaseline) {
            VStack(alignment: .leading, spacing: 4) {
                Text(detail.title.isEmpty ? detail.frontmatter.meetingID : detail.title)
                    .symairaText(.heading)
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
                .symairaText(.caption)
                .foregroundColor(SymairaTheme.textSecondary)
            }
            Spacer()
            reviewStateBadge
            if model.isRefreshing || model.isSavingReview {
                ProgressView().controlSize(.small)
            } else {
                Button("Refresh Transcript") {
                    Task { await model.refreshSelected(apply: true) }
                }
                .buttonStyle(SymairaSecondaryButtonStyle())
                .controlSize(.small)

                Button("Publish to Memory…") { showPublishSheet = true }
                    .buttonStyle(SymairaSecondaryButtonStyle())
                    .controlSize(.small)
                    .accessibilityLabel("Review and publish meeting knowledge to Memory")
                    .accessibilityHint("Shows the exact writes before anything is applied")

                if reviewState != "reviewed" {
                    Button("Mark Reviewed") {
                        Task { await model.markReviewed() }
                    }
                    .buttonStyle(SymairaPrimaryButtonStyle())
                    .controlSize(.small)
                    .keyboardShortcut("s", modifiers: [.command])
                    .accessibilityLabel("Mark this meeting as reviewed")
                    .accessibilityHint("Saves a recoverable history snapshot of the note first")
                }
            }
        }
        .padding(16)
    }

    private var reviewState: String {
        model.selectedDetail?.frontmatter.symmeetSource?.reviewState ?? ""
    }

    private var reviewStateBadge: some View {
        let state = reviewState.isEmpty ? "unreviewed" : reviewState
        let color: Color = state == "reviewed" ? .green : SymairaTheme.goldSecondary
        return Text(state)
            .symairaText(.caption).fontWeight(.semibold)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(color.opacity(0.15))
            .foregroundColor(color)
            .cornerRadius(4)
            .accessibilityLabel("Review state: \(state)")
    }

    private static func formatDuration(_ ms: Int64) -> String {
        let totalSeconds = Int(ms / 1000)
        let minutes = totalSeconds / 60
        let seconds = totalSeconds % 60
        return String(format: "%d:%02d", minutes, seconds)
    }
}
