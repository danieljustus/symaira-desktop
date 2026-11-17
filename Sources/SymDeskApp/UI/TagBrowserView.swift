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
                SymairaIconView(.search, size: 14)
                    .foregroundStyle(SymairaTokens.ink3)
                TextField("Filter tags…", text: $filterText)
                    .textFieldStyle(.plain)
                    .symairaText(.callout)
                if !filterText.isEmpty {
                    Button(action: { filterText = "" }) {
                        SymairaIconView(.close, size: 14)
                            .foregroundStyle(SymairaTokens.ink3)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Clear Filter")
                    .help("Clear Filter")
                }
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 6)

            // Sort toggle
            HStack(spacing: 4) {
                Button(action: { sortByCount = true }) {
                    Text("Count")
                        .underline(sortByCount)
                        .symairaText(.caption, respectsForeground: false)
                        .foregroundStyle(sortByCount ? SymairaTokens.ink : SymairaTokens.ink3)
                }
                .buttonStyle(.plain)
                Text("·")
                    .symairaText(.caption)
                Button(action: { sortByCount = false }) {
                    Text("A–Z")
                        .underline(!sortByCount)
                        .symairaText(.caption, respectsForeground: false)
                        .foregroundStyle(!sortByCount ? SymairaTokens.ink : SymairaTokens.ink3)
                }
                .buttonStyle(.plain)
                Spacer()
                Text("\(filteredTags.count) tag\(filteredTags.count == 1 ? "" : "s")")
                    .monospacedDigit()
                    .symairaText(.caption)
            }
            .padding(.horizontal, 8)
            .padding(.bottom, 4)

            // Tag list
            if orderedRows.isEmpty {
                VStack(spacing: 6) {
                    SymairaIconView(.tag, size: 20)
                        .foregroundStyle(SymairaTokens.ink3)
                        .accessibilityHidden(true)
                    Text(tags.isEmpty ? "No tags yet" : "No tags match")
                        .symairaText(.caption)
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
                SymairaIconView(.tag, size: 14)
                    .foregroundStyle(indent ? SymairaTokens.ink3 : SymairaTokens.ink2)
                Text(tag.name)
                    .symairaText(.callout)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer()
                Text("\(tag.count)")
                    .monospacedDigit()
                    .symairaText(.secondary)
                    .padding(.horizontal, 5)
                    .padding(.vertical, 1)
                    .background(SymairaTokens.raised)
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
                Label { Text("Rename Tag…") } icon: { Image(symairaIcon: .edit, size: 16) }
            }
            if let onMergeTag, tags.count > 1 {
                Button {
                    pendingMergeSource = tag
                } label: {
                    Label { Text("Merge Into…") } icon: { Image(symairaIcon: .merge, size: 16) }
                }
            }
            Divider()
            if let onDeleteTag {
                Button(role: .destructive) {
                    pendingDelete = tag
                } label: {
                    Label { Text("Delete Tag") } icon: { Image(symairaIcon: .delete, size: 16) }
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
                SymairaIconView(.edit, size: 16)
                    .foregroundStyle(SymairaTokens.ink2)
                VStack(alignment: .leading, spacing: 4) {
                    Text("Rename Tag").symairaText(.title)
                    Text("“\(currentName)” is renamed in every file that carries it and re-indexed.")
                        .symairaText(.secondary)
                }
            }

            LabeledContent("New name") {
                TextField("new-tag-name", text: $newName)
                    .textFieldStyle(.symaira)
                    .frame(width: 330)
            }
            .padding(18)
            .symairaCard(padding: 0)

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                    .symairaButtonStyle(.secondary)
                    .keyboardShortcut(.cancelAction)
                Button("Rename Tag") {
                    let trimmed = newName.trimmingCharacters(in: .whitespaces)
                    guard !trimmed.isEmpty, trimmed != currentName else { return }
                    isWorking = true
                    Task {
                        await onRename(trimmed)
                        dismiss()
                    }
                }
                .symairaButtonStyle(.primary)
                .keyboardShortcut(.defaultAction)
                .disabled(isWorking || newName.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(28)
        .frame(width: 590)
        .background(SymairaTokens.canvas)
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
                SymairaIconView(.merge, size: 16)
                    .foregroundStyle(SymairaTokens.ink2)
                VStack(alignment: .leading, spacing: 4) {
                    Text("Merge Tag").symairaText(.title)
                    Text("“\(sourceName)” is merged into the target tag across the whole vault and re-indexed.")
                        .symairaText(.secondary)
                }
            }

            VStack(alignment: .leading, spacing: 8) {
                LabeledContent("Into") {
                    TextField("target tag", text: $target)
                        .textFieldStyle(.symaira)
                        .frame(width: 330)
                }
                if !matchingTargets.isEmpty {
                    HStack(spacing: 6) {
                        ForEach(matchingTargets, id: \.self) { candidate in
                            Button {
                                target = candidate
                            } label: {
                                Text(candidate)
                                    .symairaText(.caption, respectsForeground: false)
                                    .foregroundStyle(target == candidate ? SymairaTokens.onSelected : SymairaTokens.ink2)
                                    .padding(.horizontal, 6)
                                    .padding(.vertical, 2)
                                    .background(target == candidate ? SymairaTokens.selected : SymairaTokens.raised)
                                    .cornerRadius(4)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
            .padding(18)
            .symairaCard(padding: 0)

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                    .symairaButtonStyle(.secondary)
                    .keyboardShortcut(.cancelAction)
                Button("Merge Tag") {
                    let trimmed = target.trimmingCharacters(in: .whitespaces)
                    guard !trimmed.isEmpty, trimmed != sourceName else { return }
                    isWorking = true
                    Task {
                        await onMerge(trimmed)
                        dismiss()
                    }
                }
                .symairaButtonStyle(.primary)
                .keyboardShortcut(.defaultAction)
                .disabled(isWorking || target.trimmingCharacters(in: .whitespaces).isEmpty || target.trimmingCharacters(in: .whitespaces) == sourceName)
            }
        }
        .padding(28)
        .frame(width: 590)
        .background(SymairaTokens.canvas)
    }
}
