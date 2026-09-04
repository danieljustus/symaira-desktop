import SwiftUI
import SymairaTheme
import SymDeskCore

/// Sidebar view for ContentView, containing navigation sections,
/// note hierarchy tree, saved views, and vault controls.
struct ContentSidebarView: View {
    @ObservedObject var model: ContentViewModel
    @EnvironmentObject var core: DeskCore

    @AppStorage("sidebar.library.collapsed") private var isLibrarySectionCollapsed = true
    @AppStorage("sidebar.tags.collapsed") private var isTagsSectionCollapsed = true
    @AppStorage("sidebar.meetings.collapsed") private var isMeetingsSectionCollapsed = true
    @AppStorage("sidebar.discover.collapsed") private var isDiscoverSectionCollapsed = true
    @AppStorage("sidebar.inbox.collapsed") private var isInboxSectionCollapsed = true
    @AppStorage("sidebar.safety.collapsed") private var isSafetySectionCollapsed = true
    @AppStorage("sidebar.settings.collapsed") private var isSettingsSectionCollapsed = true
    @AppStorage("sidebar.views.collapsed") private var isViewsSectionCollapsed = true
    @AppStorage("sidebar.savedViews.collapsed") private var isSavedViewsSectionCollapsed = true
    @AppStorage("sidebar.notes.collapsed") private var isNotesSectionCollapsed = false

    var body: some View {
        VStack(spacing: 0) {
            sidebarHeader
            List {
                // Notes are the primary workspace: keep them directly
                // below the vault controls instead of below every
                // secondary destination (issue #657).
                SidebarDisclosureSection(
                    title: "Notes",
                    systemImage: "doc.text",
                    isCollapsed: $isNotesSectionCollapsed
                ) {
                    if model.folderTree.isEmpty {
                        Text("No notes")
                            .foregroundColor(SymairaTheme.textMuted)
                    } else {
                        ForEach(model.folderTree) { node in
                            sidebarTreeNode(node)
                        }
                    }
                }

                Section {
                    Button { model.navigate(to: .dashboard) } label: {
                        Label("Dashboard", systemImage: "rectangle.grid.1x2")
                    }
                }

                SidebarDisclosureSection(
                    title: "Library",
                    systemImage: "books.vertical",
                    isCollapsed: $isLibrarySectionCollapsed
                ) {
                    HStack(spacing: 8) {
                        Button {
                            model.navigate(to: .docs, docFilter: "all")
                        } label: {
                            Label("All Documents", systemImage: "doc.on.doc")
                        }
                        .buttonStyle(.plain)
                        Spacer(minLength: 4)
                        Text("\(model.docTotalCount)")
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textSecondary)
                        Menu {
                            ForEach(DocFilterPreset.defaults) { preset in
                                Button {
                                    model.navigate(to: .docs, docFilter: preset.id)
                                } label: {
                                    let count = preset.displayCount(
                                        statusCounts: model.docCounts,
                                        typeCounts: model.docTypeCounts,
                                        total: model.docTotalCount
                                    )
                                    Text("\(preset.label) (\(count))")
                                }
                            }
                        } label: {
                            Label("Filter Library", systemImage: "line.3.horizontal.decrease.circle")
                                .labelStyle(.iconOnly)
                        }
                        .menuStyle(.borderlessButton)
                        .help("Filter Library")
                        .accessibilityLabel("Filter Library")
                    }
                }

                SidebarDisclosureSection(
                    title: "Tags",
                    systemImage: "number",
                    isCollapsed: $isTagsSectionCollapsed
                ) {
                    TagBrowserView(
                        tags: model.tagCounts,
                        onTagClick: { tag in
                            model.navigate(to: .docs, tagFilter: tag)
                        },
                        onRenameTag: { old, new in
                            await model.runTagOperation({
                                try await core.renameTag(from: old, to: new)
                            }, core: core)
                        },
                        onMergeTag: { from, into in
                            await model.runTagOperation({
                                try await core.mergeTag(from: from, into: into)
                            }, core: core)
                        },
                        onDeleteTag: { tag in
                            await model.runTagOperation({
                                try await core.deleteTag(tag)
                            }, core: core)
                        }
                    )
                    .frame(minHeight: 120)
                }

                meetingsSidebarSection

                SidebarDisclosureSection(
                    title: "Discover",
                    systemImage: "sparkles",
                    isCollapsed: $isDiscoverSectionCollapsed
                ) {
                    Button { model.navigate(to: .discover) } label: {
                        Label("Discover", systemImage: "sparkles")
                    }
                    Button { model.navigate(to: .notebooks) } label: {
                        Label("Notebooks", systemImage: "books.vertical")
                    }
                    Button { model.navigate(to: .retrievalStatus) } label: {
                        Label("Search Index", systemImage: "magnifyingglass.circle")
                    }
                    Button { model.navigate(to: .room) } label: {
                        Label("Project Journal", systemImage: "door.left.hand.open")
                    }
                    Button { model.navigate(to: .companionTools) } label: {
                        Label("Companion Tools", systemImage: "wrench.and.screwdriver")
                    }
                }

                SidebarDisclosureSection(
                    title: "Inbox & Processing",
                    systemImage: "tray.and.arrow.down",
                    isCollapsed: $isInboxSectionCollapsed
                ) {
                    Button { model.navigate(to: .ingestQueue) } label: {
                        Label("Ingest Queue", systemImage: "tray.and.arrow.down")
                    }
                    Button { model.navigate(to: .reviewLane) } label: {
                        Label("Review Lane", systemImage: "exclamationmark.triangle")
                    }
                }

                SidebarDisclosureSection(
                    title: "Safety Net",
                    systemImage: "shield",
                    isCollapsed: $isSafetySectionCollapsed
                ) {
                    Button { model.navigate(to: .history) } label: {
                        Label("Version History", systemImage: "clock.arrow.circlepath")
                    }
                    Button { model.navigate(to: .trash) } label: {
                        Label("Trash", systemImage: "trash")
                    }
                    Button { model.navigate(to: .duplicates) } label: {
                        Label("Possible Duplicates", systemImage: "arrow.triangle.2.circlepath")
                    }
                }

                SidebarDisclosureSection(
                    title: "Settings",
                    systemImage: "gearshape",
                    isCollapsed: $isSettingsSectionCollapsed
                ) {
                    Button { model.navigate(to: .rules) } label: {
                        Label("Rules & Settings", systemImage: "gearshape")
                    }
                    // Local Models stays available in code for its
                    // eventual launch, but is intentionally not a
                    // navigation row while its catalog is empty.
                }

                SidebarDisclosureSection(
                    title: "Views",
                    systemImage: "square.grid.2x2",
                    isCollapsed: $isViewsSectionCollapsed
                ) {
                    Button("Vault") { model.navigate(to: .vault) }
                    Button("Graph") { model.navigate(to: .graph) }
                }

                SidebarDisclosureSection(
                    title: "Saved Views",
                    systemImage: "bookmark",
                    isCollapsed: $isSavedViewsSectionCollapsed
                ) {
                    ForEach(model.dbViews) { view in
                        Button(view.name) {
                            model.navigate(to: .dbView, viewID: view.id)
                        }
                        .contextMenu {
                            Button("Edit View") {
                                model.editingDbView = view
                                model.isShowingViewEditor = true
                            }
                            Button("Delete View", role: .destructive) {
                                Task { await model.deleteView(view, core: core) }
                            }
                            .disabled(model.mutationTracker.isInFlight(model.viewDeleteActionID(view)))
                        }
                        .asyncActionAlert(model.mutationTracker, id: model.viewDeleteActionID(view), title: "Couldn't Delete View") {
                            Task { await model.deleteView(view, core: core) }
                        }
                    }
                    Button {
                        model.editingDbView = nil
                        model.isShowingViewEditor = true
                    } label: {
                        Label("New View", systemImage: "plus")
                    }
                }
            }
            .scrollContentBackground(.hidden)
            .listStyle(.sidebar)
            .buttonStyle(.plain)
        }
        .frame(minWidth: 240, idealWidth: 268)
        .background(.clear)
    }

    /// Fixed sidebar header: vault switcher + New Note button. Split out of
    /// the sidebar body so the type-checker stays within budget (#293, #296).
    private var sidebarHeader: some View {
        HStack {
            VaultSwitcherView()
            Spacer(minLength: 8)
            Button(action: { model.isShowingNewNoteSheet = true }) {
                Label("New Note", systemImage: "plus")
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.small)
            .tint(SymairaTheme.goldPrimary)
            // Keep the primary action's label readable whatever the vault is
            // called; the switcher truncates instead (issue #445).
            .fixedSize(horizontal: true, vertical: false)
            .layoutPriority(1)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
    }

    /// Split out of the sidebar `List` body so every destination section has
    /// the same persisted disclosure behavior (issue #657).
    private var meetingsSidebarSection: some View {
        SidebarDisclosureSection(
            title: "Meetings",
            systemImage: "person.wave.2",
            isCollapsed: $isMeetingsSectionCollapsed
        ) {
            Button { model.navigate(to: .meetings) } label: {
                Label("Meetings", systemImage: "person.wave.2")
            }
        }
    }

    /// Recursively renders a folder tree node (folder or note leaf) in the sidebar.
    @ViewBuilder
    private func sidebarTreeNode(_ node: FolderNode) -> some View {
        if node.isFolder {
            DisclosureGroup(
                isExpanded: Binding(
                    get: { model.expandedFolders.contains(node.id) },
                    set: { isExpanded in
                        if isExpanded {
                            model.expandedFolders.insert(node.id)
                        } else {
                            model.expandedFolders.remove(node.id)
                        }
                    }
                ),
                content: {
                    ForEach(node.children) { child in
                        AnyView(sidebarTreeNode(child))
                    }
                },
                label: {
                    HStack(spacing: 4) {
                        Image(systemName: "folder")
                            .foregroundColor(SymairaTheme.goldPrimary)
                        Text(node.name)
                            .foregroundColor(SymairaTheme.textPrimary)
                    }
                }
            )
        } else {
            Button {
                if let note = node.note {
                    model.navigate(to: .vault, note: note)
                }
            } label: {
                if let folder = node.containingFolder {
                    VStack(alignment: .leading, spacing: 1) {
                        Text(node.name)
                            .foregroundColor(SymairaTheme.textPrimary)
                            .lineLimit(1)
                            .truncationMode(.tail)
                        Text(folder)
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textMuted)
                            .lineLimit(1)
                            .truncationMode(.tail)
                    }
                } else {
                    Text(node.name)
                        .foregroundColor(SymairaTheme.textPrimary)
                }
            }
            .contextMenu {
                if let note = node.note {
                    Button {
                        model.historyInitialNotePath = note.path
                        model.navigate(to: .history)
                    } label: {
                        Label("Show Version History", systemImage: "clock.arrow.circlepath")
                    }
                    Divider()
                    Button(role: .destructive) {
                        model.pendingTrashNote = note
                    } label: {
                        Label("Move to Trash", systemImage: "trash")
                    }
                }
            }
        }
    }
}

/// A compact, accessible sidebar section whose disclosure state is stored in
/// the parent's `@AppStorage` binding.
struct SidebarDisclosureSection<Content: View>: View {
    let title: String
    let systemImage: String
    @Binding var isCollapsed: Bool
    @ViewBuilder let content: () -> Content

    init(
        title: String,
        systemImage: String,
        isCollapsed: Binding<Bool>,
        @ViewBuilder content: @escaping () -> Content
    ) {
        self.title = title
        self.systemImage = systemImage
        self._isCollapsed = isCollapsed
        self.content = content
    }

    var body: some View {
        Section {
            DisclosureGroup(
                isExpanded: Binding(
                    get: { !isCollapsed },
                    set: { isCollapsed = !$0 }
                )
            ) {
                content()
            } label: {
                Label(title, systemImage: systemImage)
                    .symairaText(.subheading).fontWeight(.semibold)
                    .foregroundStyle(SymairaTheme.textPrimary)
                    .accessibilityValue(isCollapsed ? "Collapsed" : "Expanded")
            }
        }
    }
}
