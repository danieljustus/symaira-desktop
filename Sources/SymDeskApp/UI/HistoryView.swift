import SwiftUI
import SymairaTheme
import SymDeskCore

// MARK: - HistoryView

struct HistoryView: View {
    @EnvironmentObject var core: DeskCore

    @State private var notes: [Note] = []
    @State private var selectedNote: Note? = nil
    @State private var versions: [HistoryEntry] = []
    @State private var isLoadingNotes = false
    @State private var isLoadingVersions = false
    @State private var errorMessage: String? = nil
    @State private var searchText = ""

    private var filteredNotes: [Note] {
        if searchText.isEmpty { return notes }
        return notes.filter {
            $0.title.localizedCaseInsensitiveContains(searchText)
                || $0.path.localizedCaseInsensitiveContains(searchText)
        }
    }

    var body: some View {
        HSplitView {
            // Note list
            noteList
                .frame(minWidth: 220, idealWidth: 260)

            // Version history for selected note
            versionList
                .frame(minWidth: 360, idealWidth: .infinity)
        }
        .navigationTitle("Version History")
        .task { await loadNotes() }
        .onChange(of: selectedNote?.id) { _, _ in
            Task { await loadVersions() }
        }
    }

    // MARK: - Note List

    private var noteList: some View {
        VStack(spacing: 0) {
            HStack {
                Image(systemName: "doc.text")
                    .foregroundColor(SymairaTheme.goldPrimary)
                Text("Notes")
                    .symairaText(.subheading)
                    .foregroundColor(SymairaTheme.textPrimary)
                Spacer()
                if isLoadingNotes {
                    ProgressView()
                        .controlSize(.small)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)

            HStack {
                Image(systemName: "magnifyingglass")
                    .foregroundColor(SymairaTheme.textMuted)
                TextField("Filter notes…", text: $searchText)
                    .textFieldStyle(.plain)
                    .symairaText(.callout)
                    .foregroundColor(SymairaTheme.textPrimary)
            }
            .padding(8)
            .background(SymairaTheme.bgDark.opacity(0.15))
            .cornerRadius(6)
            .padding(.horizontal, 12)
            .padding(.bottom, 8)

            if filteredNotes.isEmpty {
                Spacer()
                ContentUnavailableView {
                    Label("No Notes", systemImage: "doc.text")
                } description: {
                    Text(searchText.isEmpty ? "No notes found in vault." : "No notes match your filter.")
                }
                .foregroundColor(SymairaTheme.textMuted)
                Spacer()
            } else {
                List(filteredNotes) { note in
                    Button(action: { selectedNote = note }) {
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(note.title)
                                    .symairaText(.body).fontWeight(.medium)
                                    .foregroundColor(SymairaTheme.textPrimary)
                                    .lineLimit(1)
                                Text(note.path)
                                    .symairaText(.caption)
                                    .foregroundColor(SymairaTheme.textMuted)
                                    .lineLimit(1)
                            }
                            Spacer()
                            if selectedNote?.id == note.id {
                                Image(systemName: "chevron.right")
                                    .symairaText(.caption)
                                    .foregroundColor(SymairaTheme.goldPrimary)
                            }
                        }
                        .padding(.vertical, 4)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
                .scrollContentBackground(.hidden)
            }
        }
        .background(SymairaTheme.bgDark)
    }

    // MARK: - Version List

    private var versionList: some View {
        VStack(alignment: .leading, spacing: 0) {
            if let note = selectedNote {
                HStack {
                    Image(systemName: "clock.arrow.circlepath")
                        .foregroundColor(SymairaTheme.goldPrimary)
                    Text(note.title)
                        .symairaText(.title).fontWeight(.semibold)
                        .foregroundColor(SymairaTheme.textPrimary)
                        .lineLimit(1)
                }
                .padding(.horizontal, 20)
                .padding(.top, 16)
                .padding(.bottom, 4)

                Text(note.path)
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
                    .padding(.horizontal, 20)
                    .padding(.bottom, 12)

                if let error = errorMessage {
                    HStack(spacing: 8) {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .foregroundStyle(.orange)
                        Text(error)
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textSecondary)
                        Spacer()
                    }
                    .padding(10)
                    .background(Color.orange.opacity(0.12))
                    .cornerRadius(6)
                    .padding(.horizontal, 20)
                    .padding(.bottom, 8)
                }

                if isLoadingVersions {
                    Spacer()
                    ProgressView("Loading versions…")
                        .tint(SymairaTheme.goldPrimary)
                        .foregroundColor(SymairaTheme.textSecondary)
                    Spacer()
                } else if versions.isEmpty {
                    Spacer()
                    ContentUnavailableView {
                        Label("No History", systemImage: "clock")
                    } description: {
                        Text("No snapshots recorded for this note.")
                    }
                    .foregroundColor(SymairaTheme.textMuted)
                    Spacer()
                } else {
                    ScrollView {
                        VStack(spacing: 6) {
                            ForEach(Array(versions.enumerated()), id: \.element.id) { idx, version in
                                versionRow(version, isLatest: idx == 0)
                            }
                        }
                        .padding(16)
                    }
                }
            } else {
                Spacer()
                ContentUnavailableView {
                    Label("No Note Selected", systemImage: "doc.text.magnifyingglass")
                } description: {
                    Text("Select a note from the list to see its version history.")
                }
                .foregroundColor(SymairaTheme.textMuted)
                Spacer()
            }
        }
        .background(SymairaTheme.bgDarker)
    }

    private func versionRow(_ version: HistoryEntry, isLatest: Bool) -> some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    if isLatest {
                        Text("Current")
                            .symairaText(.caption).fontWeight(.bold)
                            .foregroundColor(.black)
                            .padding(.horizontal, 5)
                            .padding(.vertical, 2)
                            .background(SymairaTheme.goldPrimary)
                            .cornerRadius(3)
                    }
                    Text(formattedTimestamp(version.timestamp))
                        .symairaText(.body).fontWeight(.medium)
                        .foregroundColor(SymairaTheme.textPrimary)
                    Spacer()
                }
                HStack(spacing: 4) {
                    Text(version.snapshotID.prefix(12))
                        .symairaText(.caption).monospaced()
                        .foregroundColor(SymairaTheme.textMuted)
                    Text("·")
                        .foregroundColor(SymairaTheme.textMuted)
                    Text(formattedSize(version.size))
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                }
            }

            if !isLatest {
                Button("Restore") {
                    Task { await restoreVersion(version) }
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
                .tint(SymairaTheme.goldPrimary)
                .disabled(isLoadingVersions)
            }
        }
        .padding(12)
        .background(SymairaTheme.bgDark.opacity(0.3))
        .cornerRadius(8)
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(SymairaTheme.borderGlassHover, lineWidth: 0.5)
        )
    }

    // MARK: - Actions

    private func loadNotes() async {
        isLoadingNotes = true
        defer { isLoadingNotes = false }
        do {
            notes = try await core.listFiles()
        } catch {
            errorMessage = "Failed to load notes: \(error.localizedDescription)"
        }
    }

    private func loadVersions() async {
        guard let note = selectedNote else {
            versions = []
            return
        }
        isLoadingVersions = true
        errorMessage = nil
        defer { isLoadingVersions = false }
        do {
            versions = try await core.historyList(path: note.path)
        } catch {
            errorMessage = "Failed to load versions: \(error.localizedDescription)"
            versions = []
        }
    }

    private func restoreVersion(_ version: HistoryEntry) async {
        guard let note = selectedNote else { return }
        isLoadingVersions = true
        errorMessage = nil
        defer { isLoadingVersions = false }
        do {
            try await core.historyRestore(path: note.path, at: version.snapshotID)
            // Reload versions after restore — the restore creates a new snapshot
            // of the pre-restore state, so the list should reflect that.
            versions = try await core.historyList(path: note.path)
        } catch {
            errorMessage = "Restore failed: \(error.localizedDescription)"
        }
    }

    // MARK: - Formatting

    private func formattedTimestamp(_ raw: String) -> String {
        // Try ISO 8601 first, then fall back
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = formatter.date(from: raw) {
            let display = DateFormatter()
            display.dateStyle = .medium
            display.timeStyle = .medium
            return display.string(from: date)
        }
        // Try without fractional seconds
        formatter.formatOptions = [.withInternetDateTime]
        if let date = formatter.date(from: raw) {
            let display = DateFormatter()
            display.dateStyle = .medium
            display.timeStyle = .medium
            return display.string(from: date)
        }
        return raw
    }

    private func formattedSize(_ size: Int64) -> String {
        let formatter = ByteCountFormatter()
        formatter.countStyle = .file
        return formatter.string(fromByteCount: size)
    }
}
