import AppKit
import SwiftUI
import SymroomKit

/// Root dashboard: room picker, three panes (journal, participants, pending
/// approvals) and the install tile when the CLI is missing.
struct RoomDashboardView: View {
    @Environment(RoomAppState.self) private var appState

    var body: some View {
        Group {
            if !appState.isInstalled {
                InstallTileView()
            } else if appState.roomDirectory == nil {
                RoomPickerView()
            } else if let snapshot = appState.snapshot {
                RoomContentView(snapshot: snapshot)
            } else if appState.isLoading {
                ProgressView("Loading room…")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                VStack(spacing: 12) {
                    Text("Room could not be loaded")
                        .font(.headline)
                    if let error = appState.lastError {
                        Text(error)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .multilineTextAlignment(.center)
                    }
                    Button("Choose another folder") {
                        pickRoom()
                    }
                }
                .padding()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .task(id: appState.roomDirectory) {
            if appState.roomDirectory != nil { await appState.refresh() }
        }
    }

    private func pickRoom() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        panel.prompt = "Select Room"
        panel.message = "Select the folder that contains the room (.symroom)."
        if panel.runModal() == .OK, let url = panel.url {
            Task { await appState.select(roomDirectory: url.path) }
        }
    }
}

/// Shown when the `symroom` CLI is not installed: module renders an install
/// tile instead of the room UI (module integration contract).
private struct InstallTileView: View {
    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: "door.left.hand.open")
                .font(.system(size: 40))
                .foregroundStyle(.secondary)
            Text("symroom is not installed")
                .font(.headline)
            Text("Install it via 'brew install danieljustus/tap/symroom' to use the room module.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
        .padding(32)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

/// Shown before a room folder has been selected.
private struct RoomPickerView: View {
    @Environment(RoomAppState.self) private var appState

    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: "folder.badge.questionmark")
                .font(.system(size: 40))
                .foregroundStyle(.secondary)
            Text("No room selected")
                .font(.headline)
            Text("Pick the folder that contains the room you want to inspect.")
                .font(.caption)
                .foregroundStyle(.secondary)
            Button("Choose Room Folder…") {
                let panel = NSOpenPanel()
                panel.canChooseFiles = false
                panel.canChooseDirectories = true
                panel.allowsMultipleSelection = false
                if panel.runModal() == .OK, let url = panel.url {
                    Task { await appState.select(roomDirectory: url.path) }
                }
            }
            .buttonStyle(.borderedProminent)
        }
        .padding(32)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

/// Three-pane layout once a room snapshot is loaded.
private struct RoomContentView: View {
    let snapshot: RoomSnapshot
    @State private var selectedTab = 0

    var body: some View {
        VStack(spacing: 0) {
            Picker("View", selection: $selectedTab) {
                Text("Journal").tag(0)
                Text("Participants (\(snapshot.members.count))").tag(1)
                Text("Pending Approvals (\(snapshot.pendingRuns.count))").tag(2)
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .padding(8)

            Divider()

            switch selectedTab {
            case 0: JournalView(events: snapshot.journal)
            case 1: ParticipantsView(members: snapshot.members)
            default: ApprovalsView(runs: snapshot.pendingRuns)
            }
        }
    }
}
