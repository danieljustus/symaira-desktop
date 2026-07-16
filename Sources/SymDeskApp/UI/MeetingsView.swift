import SwiftUI
import SymDeskCore
import SymairaTheme

/// Meeting library: imported meeting notes plus SymMeet meetings still
/// available to import. Selecting an imported meeting loads it in
/// `MeetingReviewView` on the right.
struct MeetingsView: View {
    @EnvironmentObject var core: DeskCore
    @StateObject private var model: MeetingReviewModel
    @State private var showRecordingInfo = false
    @State private var recordingInfoMessage = ""

    init(dataSource: MeetingsDataSource? = nil) {
        // `dataSource` is only ever overridden by tests/previews; production
        // call sites resolve `DeskCore.shared` here rather than threading it
        // through `ContentView`, matching how every other workspace view in
        // this app reaches the core via `@EnvironmentObject`.
        _model = StateObject(wrappedValue: MeetingReviewModel(dataSource: dataSource ?? DeskCore.shared))
    }

    var body: some View {
        HStack(spacing: 0) {
            libraryLane
                .frame(minWidth: 320, maxWidth: 420)

            Divider()

            if model.selectedPath != nil {
                MeetingReviewView(model: model)
            } else {
                noSelectionState
            }
        }
        .task { await model.loadLibrary() }
    }

    private var libraryLane: some View {
        VStack(spacing: 0) {
            header
            Divider()

            List {
                if !model.importedMeetings.isEmpty {
                    Section("Meetings") {
                        ForEach(model.importedMeetings) { meeting in
                            meetingRow(meeting)
                        }
                    }
                }

                Section("Import Existing SymMeet Meeting") {
                    if let error = model.availableMeetingsError {
                        Text(error)
                            .font(.caption)
                            .foregroundColor(SymairaTheme.textMuted)
                    } else if model.availableMeetings.isEmpty {
                        Text("No new meetings to import.")
                            .font(.caption)
                            .foregroundColor(SymairaTheme.textMuted)
                    } else {
                        ForEach(model.availableMeetings) { meeting in
                            availableRow(meeting)
                        }
                    }
                }
            }
            .scrollContentBackground(.hidden)
            .listStyle(.sidebar)

            if let importError = model.importError {
                Text(importError)
                    .font(.caption)
                    .foregroundColor(.red)
                    .padding(8)
            }
        }
        .overlay {
            if case .loading = model.libraryState, model.importedMeetings.isEmpty {
                ProgressView("Loading meetings…")
                    .tint(SymairaTheme.goldPrimary)
            }
        }
    }

    private var header: some View {
        HStack {
            VStack(alignment: .leading, spacing: 4) {
                Text("Meetings")
                    .font(.title2)
                    .fontWeight(.bold)
                    .foregroundColor(SymairaTheme.textPrimary)
                if case .failed(let message) = model.libraryState {
                    Text(message)
                        .font(.caption)
                        .foregroundColor(.red)
                }
            }
            Spacer()
            recordingButton
            Button(action: { Task { await model.loadLibrary() } }) {
                Image(systemName: "arrow.clockwise")
            }
            .buttonStyle(SymairaSecondaryButtonStyle())
            .accessibilityLabel("Refresh meeting library")
        }
        .padding(16)
    }

    /// Requests a recording by bringing the SymMeet menu-bar agent forward.
    /// SymDesk deliberately cannot start a recording itself: capture,
    /// consent, and the recording confirmation UI all live in SymMeetAgent,
    /// so this action always ends in the agent's own confirmation flow.
    private var recordingButton: some View {
        Button(action: { requestRecording() }) {
            Label("Request Recording", systemImage: "record.circle")
        }
        .buttonStyle(SymairaSecondaryButtonStyle())
        .accessibilityLabel("Request a recording")
        .accessibilityHint("Opens the SymMeet menu-bar agent, where recording must be confirmed")
        .alert("Recording is confirmed in SymMeet", isPresented: $showRecordingInfo) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(recordingInfoMessage)
        }
    }

    private func requestRecording() {
        let bundleID = "dev.symaira.symmeet.agent"
        if let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) {
            NSWorkspace.shared.openApplication(at: url, configuration: NSWorkspace.OpenConfiguration())
            recordingInfoMessage = "SymMeet's menu-bar agent has been opened. Recording starts only after you confirm it there — SymDesk never records on its own."
        } else {
            recordingInfoMessage = "The SymMeet menu-bar agent is not installed. Install SymMeet to record meetings; SymDesk itself never captures audio."
        }
        showRecordingInfo = true
    }

    private func meetingRow(_ meeting: MeetingNoteSummary) -> some View {
        Button(action: { Task { await model.selectMeeting(path: meeting.path) } }) {
            VStack(alignment: .leading, spacing: 4) {
                HStack {
                    Text(meeting.title.isEmpty ? meeting.meetingID : meeting.title)
                        .font(.callout)
                        .foregroundColor(SymairaTheme.textPrimary)
                        .lineLimit(1)
                    Spacer()
                    reviewBadge(meeting.reviewState)
                }
                Text(meeting.startedAt)
                    .font(.caption2)
                    .foregroundColor(SymairaTheme.textMuted)
            }
            .padding(.vertical, 4)
        }
        .buttonStyle(.plain)
        .background(model.selectedPath == meeting.path ? SymairaTheme.goldPrimary.opacity(0.12) : Color.clear)
        .cornerRadius(6)
    }

    private func availableRow(_ meeting: AvailableMeeting) -> some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text(meeting.meetingID)
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textPrimary)
                Text(meeting.source)
                    .font(.caption2)
                    .foregroundColor(SymairaTheme.textMuted)
            }
            Spacer()
            if model.isImporting {
                ProgressView().controlSize(.small)
            } else {
                Button("Import") {
                    Task { await model.importMeeting(meetingID: meeting.meetingID) }
                }
                .buttonStyle(SymairaSecondaryButtonStyle())
                .controlSize(.small)
            }
        }
    }

    private func reviewBadge(_ state: String) -> some View {
        let label = state.isEmpty ? "unreviewed" : state
        let color: Color = label == "reviewed" ? .green : SymairaTheme.goldSecondary
        return Text(label)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(color.opacity(0.15))
            .foregroundColor(color)
            .cornerRadius(4)
    }

    private var noSelectionState: some View {
        VStack(spacing: 12) {
            Image(systemName: "person.wave.2")
                .font(.system(size: 48))
                .foregroundColor(SymairaTheme.textMuted)
            Text("Select a meeting to review")
                .font(.title3)
                .foregroundColor(SymairaTheme.textSecondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
