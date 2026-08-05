import SwiftUI
import SymDeskCore
import SymairaTheme

/// The synchronized transcript timeline: one row per time-coded segment,
/// with the row containing the current playback position highlighted and
/// row selection reported back so the caller can seek playback (#172's
/// "selecting a segment seeks playback"). Rows are rendered inside a
/// `LazyVStack`, so a three-hour transcript never materializes one view
/// hierarchy per word.
///
/// When the source artifact has no structured segments (symmeet absent,
/// meeting deleted), the view falls back to the note's plain transcript
/// text — segment data being unavailable is shown as exactly that, never
/// as note corruption.
struct TranscriptTimelineView: View {
    let transcript: String?
    var segments: [MeetingSegment] = []
    var segmentsError: String? = nil
    var selectedSegmentID: String? = nil
    var currentPlaybackMS: Int64? = nil
    var speakerLabels: [String: String] = [:]
    var onSelectSegment: ((MeetingSegment) -> Void)? = nil

    var body: some View {
        if segments.isEmpty {
            plainTranscript
        } else {
            segmentTimeline
        }
    }

    private var segmentTimeline: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 2) {
                    ForEach(segments) { segment in
                        segmentRow(segment)
                            .id(segment.segmentID)
                    }
                }
                .padding(12)
            }
            .onChange(of: selectedSegmentID) { _, newValue in
                if let newValue {
                    withAnimation { proxy.scrollTo(newValue, anchor: .center) }
                }
            }
        }
    }

    private func segmentRow(_ segment: MeetingSegment) -> some View {
        let isSelected = segment.segmentID == selectedSegmentID
        let isPlaying = currentPlaybackMS.map { segment.startMS <= $0 && $0 < segment.endMS } ?? false
        let speaker = speakerLabels[segment.speakerID] ?? segment.speakerID

        return Button(action: { onSelectSegment?(segment) }) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text(Self.formatTimestamp(segment.startMS))
                    .symairaText(.caption).monospacedDigit()
                    .foregroundColor(SymairaTheme.textMuted)
                    .frame(width: 56, alignment: .trailing)
                VStack(alignment: .leading, spacing: 2) {
                    Text(speaker)
                        .symairaText(.caption).fontWeight(.semibold)
                        .foregroundColor(SymairaTheme.goldSecondary)
                    Text(segment.displayText)
                        .symairaText(.body)
                        .foregroundColor(SymairaTheme.textPrimary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .padding(.vertical, 6)
            .padding(.horizontal, 8)
            .background(
                isPlaying ? SymairaTheme.goldPrimary.opacity(0.18)
                    : isSelected ? SymairaTheme.goldPrimary.opacity(0.10)
                    : Color.clear
            )
            .cornerRadius(6)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(speaker), at \(Self.formatTimestamp(segment.startMS)): \(segment.displayText)")
        .accessibilityAddTraits(isPlaying ? .isSelected : [])
    }

    private var plainTranscript: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                if let segmentsError {
                    Label("Time-coded segments unavailable: \(segmentsError)", systemImage: "clock.badge.exclamationmark")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                }
                if let transcript, !transcript.isEmpty {
                    Text(transcript)
                        .symairaText(.mono)
                        .foregroundColor(SymairaTheme.textPrimary)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                } else {
                    VStack(spacing: 8) {
                        Image(systemName: "text.badge.xmark")
                            .symairaText(.title)
                            .foregroundColor(SymairaTheme.textMuted)
                        Text("Transcript unavailable")
                            .symairaText(.callout)
                            .foregroundColor(SymairaTheme.textMuted)
                    }
                    .frame(maxWidth: .infinity, minHeight: 160)
                }
            }
            .padding(16)
        }
    }

    static func formatTimestamp(_ ms: Int64) -> String {
        let totalSeconds = Int(ms / 1000)
        let hours = totalSeconds / 3600
        let minutes = (totalSeconds % 3600) / 60
        let seconds = totalSeconds % 60
        if hours > 0 {
            return String(format: "%d:%02d:%02d", hours, minutes, seconds)
        }
        return String(format: "%d:%02d", minutes, seconds)
    }
}
