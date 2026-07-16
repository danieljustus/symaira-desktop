import SwiftUI
import AVFoundation
import SymairaTheme

/// Thin AVFoundation wrapper exposing just the transport state a review UI
/// needs (play/pause, current time, duration, seek). Kept separate from any
/// specific view so it can be driven by whichever local audio file path a
/// future backend contract resolves for a meeting's recording.
///
/// No SymMeet artifact today exposes a resolved, playable local file path
/// (`compose.MeetingManifest.AudioTracks[].RelativePath` is relative to an
/// undocumented SymMeet-internal storage root — see #172 follow-up), so
/// nothing in this app constructs a `MeetingAudioPlayerModel` with a real
/// URL yet. This type is real and independently testable so wiring it up is
/// a one-line change once that path is available.
@MainActor
final class MeetingAudioPlayerModel: ObservableObject {
    @Published private(set) var isPlaying = false
    @Published private(set) var currentTime: TimeInterval = 0
    @Published private(set) var duration: TimeInterval = 0

    private var player: AVPlayer?
    private var timeObserver: Any?

    init(url: URL? = nil) {
        if let url { load(url: url) }
    }

    func load(url: URL) {
        if let timeObserver, let player {
            player.removeTimeObserver(timeObserver)
        }
        let item = AVPlayerItem(url: url)
        let player = AVPlayer(playerItem: item)
        self.player = player
        duration = item.asset.duration.seconds.isFinite ? item.asset.duration.seconds : 0

        let interval = CMTime(seconds: 0.05, preferredTimescale: 600)
        timeObserver = player.addPeriodicTimeObserver(forInterval: interval, queue: .main) { [weak self] time in
            self?.currentTime = time.seconds
        }
    }

    func togglePlayback() {
        guard let player else { return }
        if isPlaying {
            player.pause()
        } else {
            player.play()
        }
        isPlaying.toggle()
    }

    func seek(to time: TimeInterval) {
        guard let player else { return }
        player.seek(to: CMTime(seconds: time, preferredTimescale: 600))
        currentTime = time
    }
}

/// Compact transport control bound to a `MeetingAudioPlayerModel`. Shows an
/// honest "unavailable" state instead of a broken/disabled player when no
/// local audio file has been resolved for the current meeting.
struct MeetingAudioPlayerView: View {
    @ObservedObject var model: MeetingAudioPlayerModel
    var hasSource: Bool

    var body: some View {
        if hasSource {
            HStack(spacing: 12) {
                Button(action: { model.togglePlayback() }) {
                    Image(systemName: model.isPlaying ? "pause.circle.fill" : "play.circle.fill")
                        .font(.system(size: 22))
                }
                .buttonStyle(.plain)
                .foregroundColor(SymairaTheme.goldPrimary)
                .keyboardShortcut(.space, modifiers: [])
                .accessibilityLabel(model.isPlaying ? "Pause" : "Play")

                Slider(
                    value: Binding(
                        get: { model.currentTime },
                        set: { model.seek(to: $0) }
                    ),
                    in: 0...max(model.duration, 0.01)
                )
                .accessibilityLabel("Playback position")

                Text(Self.format(model.currentTime) + " / " + Self.format(model.duration))
                    .font(.caption.monospacedDigit())
                    .foregroundColor(SymairaTheme.textSecondary)
            }
        } else {
            HStack(spacing: 8) {
                Image(systemName: "waveform.slash")
                    .foregroundColor(SymairaTheme.textMuted)
                Text("Audio unavailable for this meeting")
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
            }
            .accessibilityElement(children: .combine)
        }
    }

    private static func format(_ seconds: TimeInterval) -> String {
        guard seconds.isFinite, seconds >= 0 else { return "0:00" }
        let total = Int(seconds)
        return String(format: "%d:%02d", total / 60, total % 60)
    }
}
