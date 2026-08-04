import SwiftUI
import SymairaTheme
import SymDeskCore

/// Displays all tags in the vault with file counts, sorted by count or
/// alphabetically, with a filter field. Clicking a tag navigates to the
/// document grid filtered by that tag; a context menu offers vault-wide
/// rename, merge and delete (issue #306).
///
/// Nested tags (`invoice/2026`) are rendered as a hierarchy: the parent
/// segment is shown as a group header with its children indented beneath it,
/// the way Obsidian's tag pane does it.
struct TagBrowserView: View {
    let tags: [TagEntry]
    let onTagClick: (String) -> Void
    /// Called when the user picks a tag action. Implementations run the
    /// vault-wide operation through the core and refresh the tag list.
    var onRenameTag: ((String, String) async -> Void)? = nil
    var onMergeTag: ((String, String) async -> Void)? = nil
    var onDeleteTag: ((String) async -> Void)? = nil

    @State private var filterText = ""
    @State private var sortByCount = true
    @State private var pendingRename: TagEntry?
    @State private var pendingMergeSource: TagEntry?
    @State private var pendingDelete: TagEntry?

    // MARK: - Hierarchy

    /// The first path segment of a nested tag (`invoice` for `invoice/2026`).
    private func parentSegment(of tag: String) -> String {
        tag.split(separator: "/", maxSplits: 1).first.map(String.init) ?? tag
    }

    /// True when the tag is a nested child (contains a path separator).
    private func isNested(_ tag: TagEntry) -> Bool {
        tag.name.contains("/")
    }

    private var filteredTags: [TagEntry] {
        let result: [TagEntry]
        if filterText.isEmpty {
            result = tags
        } else {
            let q = filterText.lowercased()
            result = tags.filter { $0.name.lowercased().contains(q) }
        }
        if sortByCount {
            return result.sorted { $0.count == $1.count ? $0.name < $1.name : $0.count > $1.count }
        } else {
            return result.sorted { $0.name.localizedCompare($1.name) == .orderedAscending }
        }
    }

    /// Top-level tags (non-nested), sorted for display.
    private var topLevelTags: [TagEntry] {
        filteredTags.filter { !isNested($0) }
    }

    /// Nested tags grouped by their parent segment, preserving the sort order
    /// of the parents.
    private var nestedByParent: [(parent: String, children: [TagEntry])] {
        let nested = filteredTags.filter(isNested)
        let parentOrder = topLevelTags.map { $0.name } + nested.map { parentSegment(of: $0.name) }
        var seen = Set<String>()
        var groups: [(parent: String, children: [TagEntry])] = []
        for parent in parentOrder {
            guard !seen.contains(parent) else { continue }
            seen.insert(parent)
            let children = nested.filter { parentSegment(of: $0.name) == parent }
            if !children.isEmpty {
                groups.append((parent, children))
            }
        }
        return groups
    }

    /// Flat rows in display order: top-level tags first, then nested tags
    /// indented under their parent.
    private var orderedRows: [TagRow] {
        var rows: [TagRow] = []
        for tag in topLevelTags {
            rows.append(.tag(tag, indent: false))
        }
        for group in nestedByParent {
            for child in group.children {
                rows.append(.tag(child, indent: true))
            }
        }
        return rows
    }

    private enum TagRow: Identifiable {
        case tag(TagEntry, indent: Bool)
        var id: String { switch self { case .tag(let t, _): return t.name } }
    }

    // MARK: - Body

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Filter field
            HStack(spacing: 4) {
                Image(systemName: "magnifyingglass")
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
                TextField("Filter tags…", text: $filterText)
                    .textFieldStyle(.plain)
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textPrimary)
                if !filterText.isEmpty {
                    Button(action: { filterText = "" }) {
                        Image(systemName: "xmark.circle.fill")
                            .font(.caption)
                            .foregroundColor(SymairaTheme.textMuted)
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 6)

            // Sort toggle
            HStack(spacing: 4) {
                Button(action: { sortByCount = true }) {
                    Text("Count")
                        .font(.caption2)
                        .foregroundColor(sortByCount ? SymairaTheme.goldPrimary : SymairaTheme.textMuted)
                }
                .buttonStyle(.plain)
                Text("·")
                    .font(.caption2)
                    .foregroundColor(SymairaTheme.textMuted)
                Button(action: { sortByCount = false }) {
                    Text("A–Z")
                        .font(.caption2)
                        .foregroundColor(!sortByCount ? SymairaTheme.goldPrimary : SymairaTheme.textMuted)
                }
                .buttonStyle(.plain)
                Spacer()
                Text("\(filteredTags.count) tag\(filteredTags.count == 1 ? "" : "s")")
                    .font(.caption2)
                    .foregroundColor(SymairaTheme.textMuted)
            }
            .padding(.horizontal, 8)
            .padding(.bottom, 4)

            // Tag list
            if orderedRows.isEmpty {
                VStack(spacing: 6) {
                    Image(systemName: "tag")
                        .font(.title3)
                        .foregroundColor(SymairaTheme.textMuted)
                    Text(tags.isEmpty ? "No tags yet" : "No tags match")
                        .font(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 16)
            } else {
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 1) {
                        ForEach(orderedRows) { row in
                            if case .tag(let tag, let indent) = row {
                                tagRow(tag, indent: indent)
                            }
                        }
                    }
                }
                .scrollContentBackground(.hidden)
            }
        }
        // Rename sheet
        .sheet(item: $pendingRename) { entry in
            RenameTagSheet(currentName: entry.name) { newName in
                await runRename(entry, to: newName)
            }
        }
        // Merge sheet
        .sheet(item: $pendingMergeSource) { entry in
            MergeTagSheet(
                sourceName: entry.name,
                allTags: tags.map(\.name).filter { $0 != entry.name }
            ) { target in
                await runMerge(entry, into: target)
            }
        }
        // Delete confirmation
        .confirmationDialog(
            "Delete tag “\(pendingDelete?.name ?? "")”?",
            isPresented: Binding(
                get: { pendingDelete != nil },
                set: { if !$0 { pendingDelete = nil } }
            ),
            titleVisibility: .visible
        ) {
            Button("Delete Tag", role: .destructive) {
                if let entry = pendingDelete {
                    Task { await onDeleteTag?(entry.name) }
                }
                pendingDelete = nil
            }
            Button("Cancel", role: .cancel) { pendingDelete = nil }
        } message: {
            Text("The tag is removed from every file that carries it. Your notes are otherwise untouched.")
        }
    }

    // MARK: - Row

    @ViewBuilder
    private func tagRow(_ tag: TagEntry, indent: Bool) -> some View {
        Button(action: { onTagClick(tag.name) }) {
            HStack(spacing: 6) {
                Image(systemName: indent ? "tag" : "tag.fill")
                    .font(.caption2)
                    .foregroundColor(indent ? SymairaTheme.textMuted : SymairaTheme.goldSecondary)
                Text(tag.name)
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textPrimary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer()
                Text("\(tag.count)")
                    .font(.caption2)
                    .foregroundColor(SymairaTheme.textSecondary)
                    .padding(.horizontal, 5)
                    .padding(.vertical, 1)
                    .background(Color.white.opacity(0.06))
                    .cornerRadius(3)
            }
            .padding(.horizontal, indent ? 16 : 8)
            .padding(.vertical, 3)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .contextMenu {
            Button {
                pendingRename = tag
            } label: {
                Label("Rename Tag…", systemImage: "pencil")
            }
            if let onMergeTag, tags.count > 1 {
                Button {
                    pendingMergeSource = tag
                } label: {
                    Label("Merge Into…", systemImage: "arrow.triangle.merge")
                }
            }
            Divider()
            if let onDeleteTag {
                Button(role: .destructive) {
                    pendingDelete = tag
                } label: {
                    Label("Delete Tag", systemImage: "trash")
                }
            }
        }
    }

    // MARK: - Actions

    private func runRename(_ entry: TagEntry, to newName: String) async {
        pendingRename = nil
        await onRenameTag?(entry.name, newName)
    }

    private func runMerge(_ entry: TagEntry, into target: String) async {
        pendingMergeSource = nil
        await onMergeTag?(entry.name, target)
    }
}

/// Modal prompt for renaming a tag vault-wide.
private struct RenameTagSheet: View {
    @Environment(\.dismiss) private var dismiss
    let currentName: String
    let onRename: (String) async -> Void

    @State private var newName = ""
    @State private var isWorking = false

    var body: some View {
        VStack(alignment: .leading, spacing: 22) {
            HStack(alignment: .top, spacing: 14) {
                Image(systemName: "pencil")
                    .font(.system(size: 28))
                    .foregroundStyle(SymairaTheme.goldPrimary)
                VStack(alignment: .leading, spacing: 4) {
                    Text("Rename Tag").font(.title2.bold())
                    Text("“\(currentName)” is renamed in every file that carries it and re-indexed.")
                        .foregroundStyle(SymairaTheme.textSecondary)
                }
            }

            LabeledContent("New name") {
                TextField("new-tag-name", text: $newName)
                    .textFieldStyle(.roundedBorder)
                    .frame(width: 330)
            }
            .padding(18)
            .glassmorphicPanel()

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                    .buttonStyle(.bordered)
                Button("Rename") {
                    let trimmed = newName.trimmingCharacters(in: .whitespaces)
                    guard !trimmed.isEmpty, trimmed != currentName else { return }
                    isWorking = true
                    Task {
                        await onRename(trimmed)
                        dismiss()
                    }
                }
                .buttonStyle(.borderedProminent)
                .tint(SymairaTheme.goldPrimary)
                .disabled(isWorking || newName.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(28)
        .frame(width: 590)
        .background(SymairaTheme.bgDark)
    }
}

/// Modal picker for merging a tag into another existing tag.
private struct MergeTagSheet: View {
    @Environment(\.dismiss) private var dismiss
    let sourceName: String
    let allTags: [String]
    let onMerge: (String) async -> Void

    @State private var target = ""
    @State private var isWorking = false

    private var matchingTargets: [String] {
        let q = target.lowercased()
        return allTags
            .filter { $0.lowercased().contains(q) }
            .sorted()
            .prefix(8)
            .map { $0 }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 22) {
            HStack(alignment: .top, spacing: 14) {
                Image(systemName: "arrow.triangle.merge")
                    .font(.system(size: 28))
                    .foregroundStyle(SymairaTheme.goldPrimary)
                VStack(alignment: .leading, spacing: 4) {
                    Text("Merge Tag").font(.title2.bold())
                    Text("“\(sourceName)” is merged into the target tag across the whole vault and re-indexed.")
                        .foregroundStyle(SymairaTheme.textSecondary)
                }
            }

            VStack(alignment: .leading, spacing: 8) {
                LabeledContent("Into") {
                    TextField("target tag", text: $target)
                        .textFieldStyle(.roundedBorder)
                        .frame(width: 330)
                }
                if !matchingTargets.isEmpty {
                    HStack(spacing: 6) {
                        ForEach(matchingTargets, id: \.self) { candidate in
                            Button {
                                target = candidate
                            } label: {
                                Text(candidate)
                                    .font(.caption)
                                    .padding(.horizontal, 6)
                                    .padding(.vertical, 2)
                                    .background(target == candidate ? SymairaTheme.goldPrimary.opacity(0.25) : Color.white.opacity(0.08))
                                    .cornerRadius(4)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
            .padding(18)
            .glassmorphicPanel()

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                    .buttonStyle(.bordered)
                Button("Merge") {
                    let trimmed = target.trimmingCharacters(in: .whitespaces)
                    guard !trimmed.isEmpty, trimmed != sourceName else { return }
                    isWorking = true
                    Task {
                        await onMerge(trimmed)
                        dismiss()
                    }
                }
                .buttonStyle(.borderedProminent)
                .tint(SymairaTheme.goldPrimary)
                .disabled(isWorking || target.trimmingCharacters(in: .whitespaces).isEmpty || target.trimmingCharacters(in: .whitespaces) == sourceName)
            }
        }
        .padding(28)
        .frame(width: 590)
        .background(SymairaTheme.bgDark)
    }
}
