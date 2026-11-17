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
                SymairaIconView(.document, size: 16)
                    .foregroundStyle(SymairaTokens.ink2)
                Text("Notes")
                    .symairaText(.subheading)
                Spacer()
                if isLoadingNotes {
                    ProgressView()
                        .accessibilityLabel("Loading history…")
                        .controlSize(.small)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)

            HStack {
                SymairaIconView(.search, size: 16)
                    .foregroundStyle(SymairaTokens.ink3)
                TextField("Filter notes…", text: $searchText)
                    .textFieldStyle(.plain)
                    .symairaText(.callout)
            }
            .padding(8)
            .background(SymairaTokens.raised)
            .cornerRadius(6)
            .padding(.horizontal, 12)
            .padding(.bottom, 8)

            if filteredNotes.isEmpty {
                SymairaEmptyState(
                    icon: .document,
                    title: "No Notes",
                    message: searchText.isEmpty ? "No notes found in vault." : "No notes match your filter."
                )
            } else {
                List(filteredNotes) { note in
                    Button(action: { selectedNote = note }) {
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(note.title)
                                    .symairaText(.bodyEmphasized)
                                    .lineLimit(1)
                                Text(note.path)
                                    .symairaText(.caption)
                                    .lineLimit(1)
                            }
                            Spacer()
                            if selectedNote?.id == note.id {
                                SymairaIconView(.forward, size: 14)
                                    .foregroundStyle(SymairaTokens.ink)
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
        .background(SymairaTokens.surface)
    }

    // MARK: - Version List

    private var versionList: some View {
        VStack(alignment: .leading, spacing: 0) {
            if let note = selectedNote {
                HStack {
                    SymairaIconView(.history, size: 16)
                        .foregroundStyle(SymairaTokens.ink2)
                    Text(note.title)
                        .symairaText(.title)
                        .lineLimit(1)
                }
                .padding(.horizontal, 20)
                .padding(.top, 16)
                .padding(.bottom, 4)

                Text(note.path)
                    .symairaText(.caption)
                    .padding(.horizontal, 20)
                    .padding(.bottom, 12)

                if let error = errorMessage {
                    SymairaNotice(message: error, tone: .warning, onDismiss: { errorMessage = nil })
                        .padding(.horizontal, 20)
                    .padding(.bottom, 8)
                }

                if isLoadingVersions {
                    SymairaLoadingState("Loading versions…")
                } else if versions.isEmpty {
                    SymairaEmptyState(
                        icon: .time,
                        title: "No History",
                        message: "No snapshots recorded for this note."
                    )
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
                SymairaEmptyState(
                    icon: .documentSearch,
                    title: "No Note Selected",
                    message: "Select a note from the list to see its version history."
                )
            }
        }
        .background(SymairaTokens.canvas)
    }

    private func versionRow(_ version: HistoryEntry, isLatest: Bool) -> some View {
        let isSelected = selectedVersion?.snapshotID == version.snapshotID
        return HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    if isLatest {
                        SymairaBadge("Current", tone: .neutral, icon: .confirm)
                    }
                    Text(formattedTimestamp(version.timestamp))
                        .monospacedDigit()
                        .symairaText(.bodyEmphasized)
                    Spacer()
                }
                HStack(spacing: 4) {
                    Text(version.snapshotID.prefix(12))
                        .symairaText(.monoSmall)
                    Text("·")
                        .symairaText(.caption)
                    Text(formattedSize(version.size))
                        .monospacedDigit()
                        .symairaText(.caption)
                }
            }

            if !isLatest {
                Button("Diff") {
                    selectedVersion = version
                }
                .symairaButtonStyle(.secondary)
                .disabled(isLoadingVersions || isLoadingDiff)
                .help("Show what changed in this version")

                Button("Restore") {
                    Task { await restoreVersion(version) }
                }
                .symairaButtonStyle(.secondary)
                .disabled(isLoadingVersions)
            }
        }
        .padding(12)
        .background(isSelected ? SymairaTokens.selection : SymairaTokens.raised)
        .cornerRadius(8)
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(isSelected ? SymairaTokens.focus : SymairaTokens.line, lineWidth: isSelected ? 1 : 0.5)
        )
    }

    // MARK: - Diff Pane

    /// Shows the line-level diff between the selected version and the current
    /// file content (issue #307). The "current" baseline is re-read on every
    /// selection so a restored file immediately reflects the new state.
    private var diffPane: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                SymairaIconView(.diff, size: 16)
                    .foregroundStyle(SymairaTokens.ink2)
                Text("Diff vs. current")
                    .symairaText(.subheading)
                Spacer()
                if isLoadingDiff {
                    ProgressView()
                        .accessibilityLabel("Loading diff…")
                        .controlSize(.small)
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 10)

            SymairaFormDivider()

            if let diff = diffContent {
                if diff.lines.isEmpty {
                    VStack(spacing: 6) {
                        SymairaIconView(.success, size: 16)
                            .foregroundStyle(SymairaTokens.ink2)
                        Text("No differences — this version matches the current content.")
                            .symairaText(.secondary)
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
                        .font(SymairaTypography.monoSmall)
                    }
                    .background(SymairaTokens.surface)
                }
            } else if selectedVersion == nil {
                VStack(spacing: 6) {
                    SymairaIconView(.documentSearch, size: 16)
                        .foregroundStyle(SymairaTokens.ink3)
                    Text("Select a version and press Diff to compare it with the current file.")
                        .symairaText(.secondary)
                        .multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .padding()
            } else {
                VStack(spacing: 6) {
                    SymairaIconView(.history, size: 16)
                        .foregroundStyle(SymairaTokens.ink3)
                    Text("Loading diff…")
                        .symairaText(.secondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .background(SymairaTokens.canvas)
    }

    @ViewBuilder
    private func diffLineRow(_ line: DiffLine) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Text(line.kind == .same ? " " : (line.kind == .added ? "+" : "−"))
                .symairaText(.monoSmall)
                .foregroundStyle(line.kind == .added ? SymairaTokens.positive : (line.kind == .removed ? SymairaTokens.critical : Color.clear))
                .frame(width: 10)
            Text(line.text.isEmpty ? " " : line.text)
                .foregroundStyle(
                    line.kind == .added ? SymairaTokens.positive
                        : (line.kind == .removed ? SymairaTokens.critical : SymairaTokens.ink)
                )
            Spacer(minLength: 0)
        }
        .padding(.vertical, 1)
        .background(
            line.kind == .added ? SymairaTokens.positive.opacity(0.12)
                : (line.kind == .removed ? SymairaTokens.critical.opacity(0.12) : Color.clear)
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
