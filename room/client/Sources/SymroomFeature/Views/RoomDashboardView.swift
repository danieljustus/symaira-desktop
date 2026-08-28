import AppKit
import SwiftUI
import SymroomKit

/// Root dashboard: Project Journal folder picker, three panes (journal,
/// participants, pending approvals) and the install tile when the CLI is missing.
struct RoomDashboardView: View {
    @Environment(RoomAppState.self) private var appState

    var body: some View {
        VStack(spacing: 0) {
            if let directory = appState.provenanceDirectory {
                ProvenanceWarningView(directory: directory)
            }
            content
        }
        .task(id: appState.roomDirectory) {
            if appState.roomDirectory != nil { await appState.refresh() }
        }
    }

    @ViewBuilder
    private var content: some View {
        Group {
            if !appState.isInstalled {
                InstallTileView()
            } else if appState.roomDirectory == nil {
                RoomPickerView()
            } else if let snapshot = appState.snapshot {
                RoomContentView(snapshot: snapshot)
            } else if appState.isLoading {
                ProgressView("Loading Project Journal…")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                VStack(spacing: 12) {
                    Text("Project Journal could not be loaded")
                        .font(.headline)
                    if let error = appState.lastError {
                        Text(error)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .multilineTextAlignment(.center)
                    }
                    Button("Choose another folder") {
                        pickProjectJournal()
                    }
                }
                .padding()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
    }

    private func pickProjectJournal() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        panel.prompt = "Select Project Journal"
        panel.message = "Select the folder that contains the Project Journal data (.symroom)."
        if panel.runModal() == .OK, let url = panel.url {
            Task { await appState.select(roomDirectory: url.path) }
        }
    }
}

/// Shown when the `symroom` CLI is genuinely not found (neither the strict
/// nor the relaxed search located it): module renders an install tile
/// instead of the Project Journal UI (module integration contract).
private struct InstallTileView: View {
    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: "door.left.hand.open")
                .font(.system(size: 40))
                .foregroundStyle(.secondary)
            Text("Project Journal tools are unavailable")
                .font(.headline)
            // danieljustus/tap/symroom is disabled since v0.10.0 — symroom
            // ships inside the symdesk formula now (#608).
            Text("Install it via 'brew install danieljustus/tap/symdesk' to use Project Journal. The Project Journal helper ships with Symaira Desktop.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
        .padding(32)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

/// Warning shown when the helper was found in a folder whose permissions are
/// broader than the app's strict provenance policy. Keep the wording user-
/// facing; the single action reveals the location so it can be secured.
private struct ProvenanceWarningView: View {
    let directory: String

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(.orange)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 3) {
                Text("Project Journal needs attention")
                    .font(.headline)
                Text("The Project Journal helper was found in a folder with broad permissions. It can still run, but a more private location is safer.")
                    .font(.caption)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 8)
            Button("Show in Finder") {
                NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: directory)])
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
            .help("Show the helper's location in Finder")
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .background(Color.orange.opacity(0.12), in: RoundedRectangle(cornerRadius: 8))
        .overlay {
            RoundedRectangle(cornerRadius: 8)
                .stroke(Color.orange.opacity(0.35), lineWidth: 1)
        }
        .padding(10)
        .accessibilityElement(children: .contain)
    }
}

/// Shown before a Project Journal folder has been selected.
private struct RoomPickerView: View {
    @Environment(RoomAppState.self) private var appState

    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: "folder.badge.questionmark")
                .font(.system(size: 40))
                .foregroundStyle(.secondary)
            Text("No Project Journal folder selected")
                .font(.headline)
            Text("Choose the folder that contains the Project Journal data you want to inspect.")
                .font(.caption)
                .foregroundStyle(.secondary)
            Button("Choose Project Journal Folder…") {
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

/// Three-pane layout once a Project Journal snapshot is loaded.
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
