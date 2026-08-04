import SwiftUI
import SymairaTheme
import SymDeskCore

// MARK: - HistoryView

struct HistoryView: View {
    @EnvironmentObject var core: DeskCore

    /// When set (e.g. from a note's context menu), this note is selected
    /// automatically once the note list is loaded (issue #307).
    var initialNotePath: String? = nil

    @State private var notes: [Note] = []
    @State private var selectedNote: Note? = nil
    @State private var versions: [HistoryEntry] = []
    @State private var isLoadingNotes = false
    @State private var isLoadingVersions = false
    @State private var errorMessage: String? = nil
    @State private var searchText = ""
    @State private var selectedVersion: HistoryEntry? = nil
    @State private var diffContent: DiffResult? = nil
    @State private var isLoadingDiff = false
    @State private var hasAppliedInitialNote = false

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
            selectedVersion = nil
            diffContent = nil
            Task { await loadVersions() }
        }
        .onChange(of: selectedVersion?.snapshotID) { _, _ in
            Task { await loadDiff() }
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
                    HSplitView {
                        ScrollView {
                            VStack(spacing: 6) {
                                ForEach(Array(versions.enumerated()), id: \.element.id) { idx, version in
                                    versionRow(version, isLatest: idx == 0)
                                }
                            }
                            .padding(16)
                        }
                        .frame(minWidth: 300)

                        diffPane
                            .frame(minWidth: 320)
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
        let isSelected = selectedVersion?.snapshotID == version.snapshotID
        return HStack(spacing: 12) {
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
                Button("Diff") {
                    selectedVersion = version
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .disabled(isLoadingVersions || isLoadingDiff)
                .help("Show what changed in this version")

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
        .background(isSelected ? SymairaTheme.goldPrimary.opacity(0.10) : SymairaTheme.bgDark.opacity(0.3))
        .cornerRadius(8)
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(isSelected ? SymairaTheme.goldPrimary.opacity(0.6) : SymairaTheme.borderGlassHover, lineWidth: isSelected ? 1 : 0.5)
        )
    }

    // MARK: - Diff Pane

    /// Shows the line-level diff between the selected version and the current
    /// file content (issue #307). The "current" baseline is re-read on every
    /// selection so a restored file immediately reflects the new state.
    private var diffPane: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Image(systemName: "plus.forwardslash.minus")
                    .foregroundColor(SymairaTheme.goldPrimary)
                Text("Diff vs. current")
                    .symairaText(.subheading)
                    .foregroundColor(SymairaTheme.textPrimary)
                Spacer()
                if isLoadingDiff {
                    ProgressView()
                        .controlSize(.small)
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 10)

            Divider().overlay(SymairaTheme.borderGlass)

            if let diff = diffContent {
                if diff.lines.isEmpty {
                    VStack(spacing: 6) {
                        Image(systemName: "checkmark.circle")
                            .foregroundColor(SymairaTheme.goldSecondary)
                        Text("No differences — this version matches the current content.")
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textSecondary)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 0) {
                            ForEach(Array(diff.lines.enumerated()), id: \.offset) { _, line in
                                diffLineRow(line)
                            }
                        }
                        .padding(10)
                        .font(.system(.caption, design: .monospaced))
                    }
                    .background(SymairaTheme.bgDark.opacity(0.35))
                }
            } else if selectedVersion == nil {
                VStack(spacing: 6) {
                    Image(systemName: "doc.text.magnifyingglass")
                        .foregroundColor(SymairaTheme.textMuted)
                    Text("Select a version and press Diff to compare it with the current file.")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textSecondary)
                        .multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .padding()
            } else {
                VStack(spacing: 6) {
                    Image(systemName: "clock.arrow.circlepath")
                        .foregroundColor(SymairaTheme.textMuted)
                    Text("Loading diff…")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textSecondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .background(SymairaTheme.bgDarker)
    }

    @ViewBuilder
    private func diffLineRow(_ line: DiffLine) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Text(line.kind == .same ? " " : (line.kind == .added ? "+" : "−"))
                .font(.caption2.monospaced())
                .foregroundColor(line.kind == .added ? .green : (line.kind == .removed ? .red : .clear))
                .frame(width: 10)
            Text(line.text.isEmpty ? " " : line.text)
                .foregroundColor(
                    line.kind == .added ? Color.green.opacity(0.95)
                        : (line.kind == .removed ? Color.red.opacity(0.9) : SymairaTheme.textPrimary)
                )
            Spacer(minLength: 0)
        }
        .padding(.vertical, 1)
        .background(
            line.kind == .added ? Color.green.opacity(0.08)
                : (line.kind == .removed ? Color.red.opacity(0.08) : .clear)
        )
    }

    // MARK: - Actions

    private func loadNotes() async {
        isLoadingNotes = true
        defer { isLoadingNotes = false }
        do {
            notes = try await core.listFiles()
            // A note selected from the sidebar context menu lands here once
            // the list is loaded (issue #307).
            if !hasAppliedInitialNote, let initialNotePath,
               let match = notes.first(where: { $0.path == initialNotePath || $0.id == initialNotePath }) {
                selectedNote = match
                hasAppliedInitialNote = true
            }
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

    /// Loads the selected version's content and the current file content and
    /// computes a line-level diff between them (issue #307).
    private func loadDiff() async {
        guard let note = selectedNote, let version = selectedVersion else {
            diffContent = nil
            return
        }
        isLoadingDiff = true
        defer { isLoadingDiff = false }
        do {
            let versionText = try await core.historyContent(id: version.snapshotID)
            let currentText = (try? await core.docNoteContent(path: note.path)) ?? ""
            diffContent = Self.makeDiff(old: versionText, new: currentText)
        } catch {
            errorMessage = "Failed to load diff: \(error.localizedDescription)"
            diffContent = nil
        }
    }

    /// Simple line diff: unchanged lines are kept, added/removed lines are
    /// marked. Uses the classic two-pointer LCS-free approach which is exact
    /// for the common case and degrades gracefully for large rewrites.
    static func makeDiff(old: String, new: String) -> DiffResult {
        let oldLines = old.components(separatedBy: .newlines)
        let newLines = new.components(separatedBy: .newlines)

        // Trim trailing empty line from split behavior.
        func cleaned(_ lines: [String]) -> [String] {
            var l = lines
            while l.last == "" { l.removeLast() }
            return l
        }
        let a = cleaned(oldLines)
        let b = cleaned(newLines)

        var result: [DiffLine] = []
        var i = 0, j = 0
        while i < a.count || j < b.count {
            if i < a.count && j < b.count && a[i] == b[j] {
                result.append(DiffLine(kind: .same, text: a[i]))
                i += 1
                j += 1
                continue
            }
            // Look ahead: is this line added in the new text or removed?
            if j < b.count && (i >= a.count || !a[i...].contains(b[j])) {
                result.append(DiffLine(kind: .added, text: b[j]))
                j += 1
            } else if i < a.count {
                result.append(DiffLine(kind: .removed, text: a[i]))
                i += 1
            }
        }
        // Identical texts produce no diff at all; context lines are only kept
        // when there is at least one change to explain.
        if !result.contains(where: { $0.kind != .same }) {
            return DiffResult(lines: [])
        }
        return DiffResult(lines: result)
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

// MARK: - Diff model

enum DiffLineKind: Equatable {
    case same
    case added
    case removed
}

struct DiffLine: Equatable {
    let kind: DiffLineKind
    let text: String
}

struct DiffResult: Equatable {
    let lines: [DiffLine]
}
