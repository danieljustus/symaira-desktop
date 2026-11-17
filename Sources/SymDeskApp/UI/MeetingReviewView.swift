import SwiftUI
import SymDeskCore
import SymairaTheme

/// Detail/review pane for the currently selected meeting: metadata,
/// participants, audio transport (when available), and the transcript.
struct MeetingReviewView: View {
    @ObservedObject var model: MeetingReviewModel
    @EnvironmentObject var core: DeskCore
    @StateObject private var audioPlayer = MeetingAudioPlayerModel()
    @State private var showPublishSheet = false

    var body: some View {
        Group {
            switch model.detailState {
            case .idle, .loading:
                SymairaLoadingState("Loading meeting…")
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
                    SymairaLoadingState("Loading meeting…")
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
        }
    }

    private func failedState(_ message: String) -> some View {
        VStack(spacing: 12) {
            SymairaIconView(.warning, size: 24)
                .foregroundStyle(SymairaTokens.critical)
            if let path = model.selectedPath {
                Text("Couldn't load \(fileName(for: path))")
                    .symairaText(.heading)
                Text(path)
                    .symairaText(.monoSmall)
                    .textSelection(.enabled)
            } else {
                Text("Couldn't load this meeting")
                    .symairaText(.heading)
            }
            Text(message)
                .symairaText(.secondary)
                .multilineTextAlignment(.center)
            HStack(spacing: 10) {
                if let path = model.selectedPath {
                    Button("Retry") {
                        Task { await model.selectMeeting(path: path) }
                    }
                    .symairaButtonStyle(.secondary)
                    if canRevealFiles {
                        Button("Reveal") { reveal(path: path) }
                            .symairaButtonStyle(.secondary)
                    }
                    Button("Skip") { model.clearSelection() }
                        .symairaButtonStyle(.secondary)
                }
            }
        }
        .symairaCard(padding: SymairaSpacing.xLarge)
        .overlay {
            RoundedRectangle(cornerRadius: SymairaRadius.card)
                .stroke(SymairaTokens.critical.opacity(0.35), lineWidth: 1)
        }
        .frame(maxWidth: 520)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var canRevealFiles: Bool {
        !core.isRemote && core.vaultPath != nil
    }

    private func fileName(for path: String) -> String {
        URL(fileURLWithPath: path).lastPathComponent
    }

    private func reveal(path: String) {
        guard let vaultPath = core.vaultPath else { return }
        let fileURL = URL(fileURLWithPath: vaultPath).appendingPathComponent(path)
        NSWorkspace.shared.activateFileViewerSelecting([fileURL])
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
                    .symairaText(.caption, respectsForeground: false)
                    .foregroundStyle(SymairaTokens.critical)
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
                HStack(spacing: 10) {
                    Label(detail.frontmatter.startedAt, symairaIcon: .calendar)
                    if let language = detail.frontmatter.language, !language.isEmpty {
                        Label(language, symairaIcon: .language)
                    }
                    if let ms = detail.frontmatter.durationMS, ms > 0 {
                        Label(Self.formatDuration(ms), symairaIcon: .time)
                    }
                }
                .symairaText(.secondary)
            }
            Spacer()
            reviewStateBadge
            if model.isRefreshing || model.isSavingReview {
                ProgressView().controlSize(.small)
                    .accessibilityLabel("Loading meeting…")
            } else {
                Button("Refresh Transcript") {
                    Task { await model.refreshSelected(apply: true) }
                }
                .symairaButtonStyle(.secondary)

                Button("Publish to Memory…") { showPublishSheet = true }
                    .symairaButtonStyle(.secondary)
                    .accessibilityLabel("Review and publish meeting knowledge to Memory")
                    .accessibilityHint("Shows the exact writes before anything is applied")

                if reviewState != "reviewed" {
                    Button("Mark Reviewed") {
                        Task { await model.markReviewed() }
                    }
                    .symairaButtonStyle(.primary)
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
        return SymairaBadge(state, tone: state == "reviewed" ? .positive : .warning)
            .accessibilityLabel("Review state: \(state)")
    }

    private static func formatDuration(_ ms: Int64) -> String {
        let totalSeconds = Int(ms / 1000)
        let minutes = totalSeconds / 60
        let seconds = totalSeconds % 60
        return String(format: "%d:%02d", minutes, seconds)
    }
}
