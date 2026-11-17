import SwiftUI
import AppKit
import SymairaTheme
import SymDeskCore
import SymroomFeature

/// The main window: the shared Symaira workspace (GUIDELINES SHELL-1) with
/// the seven `DeskDestination`s in the sidebar. Multi-mode destinations show
/// a fixed-height section bar above their content (LAYOUT-7).
struct ContentView: View {
    typealias DisplayMode = ContentDisplayMode

    @EnvironmentObject var core: DeskCore
    @EnvironmentObject var watcher: EventWatcher
    @EnvironmentObject var notificationManager: NotificationManager
    @Environment(\.openSettings) private var openSettings

    @StateObject private var model = ContentViewModel()
    @AppStorage("isBlockMode") private var isBlockMode = false
    @AppStorage("settings.selectedTab") private var settingsTab: DeskSettingsTab = .general
    @State private var sidebar = SymairaSidebarState()
    @State private var topography = SymairaTopoModel()

    var body: some View {
        Group {
            if core.isReady {
                workspace
            } else {
                startupState
            }
        }
        .frame(minWidth: 840, minHeight: 560)
    }

    // MARK: - Startup

    /// Shown until the core CLI answers: loading, or the unreachable state
    /// with the next step (STATE-1, STATE-4).
    @ViewBuilder
    private var startupState: some View {
        ZStack {
            SymairaTokens.canvas.ignoresSafeArea()
            if let err = core.errorMessage {
                SymairaEmptyState(
                    icon: .warning,
                    title: "SymDesk Core Unavailable",
                    message: "\(err) "
                        + (ServerConnectionConfig.hasConnection
                            ? "Check the server URL, network and access token, then restart SymDesk."
                            : "Run `brew install danieljustus/tap/symdesk` to install the core CLI, then restart SymDesk.")
                )
                .frame(maxWidth: SymairaMetrics.readableContentWidth)
                .padding(SymairaSpacing.xLarge)
            } else {
                SymairaLoadingState("Connecting to SymDesk Core…")
            }
        }
    }

    // MARK: - Workspace

    private var destinationBinding: Binding<DeskDestination> {
        Binding(get: { model.destination }, set: { model.select($0) })
    }

    private var workspace: some View {
        SymairaWorkspace(
            sidebar: $sidebar,
            // Topography belongs behind the main overview only (SHELL-8).
            topography: model.displayMode == .dashboard ? topography : nil,
            navigation: { showsLabels in
                ContentSidebarView(model: model, showsLabels: showsLabels)
            },
            content: { workspaceContent }
        )
        .tint(SymairaTokens.selected)
        .inspector(isPresented: $model.isShowingInspector) {
            inspectorContent
                .inspectorColumnWidth(min: 240, ideal: 280, max: 420)
        }
        .onDrop(of: [.fileURL], isTargeted: nil, perform: { providers in model.handleDrop(providers, core: core) })
        .toolbar {
            SymairaWorkspaceToolbar(sidebar: $sidebar) { openSettings() }
            mainToolbar
        }
        .focusedSceneValue(\.deskDestination, destinationBinding)
        .contentSheets(model: model, core: core)
        // App-wide shortcut for Cmd-K
        .onAppear { model.installKeyEventMonitor() }
        .onDisappear { model.cleanup() }
        .onReceive(NotificationCenter.default.publisher(for: .openDiscover)) { _ in model.openDiscover() }
        .onReceive(NotificationCenter.default.publisher(for: .openDashboard)) { _ in model.openDashboard() }
        .onReceive(NotificationCenter.default.publisher(for: .openCommandPalette)) { _ in
            model.isShowingPalette = true
        }
        .onReceive(NotificationCenter.default.publisher(for: .toggleEditorPreview)) { _ in
            model.isShowingPreview.toggle()
        }
        .onReceive(NotificationCenter.default.publisher(for: .toggleEditorInspector)) { _ in
            model.toggleInspector()
        }
        .onReceive(NotificationCenter.default.publisher(for: .openEditorAIDock)) { _ in
            model.openAIDock()
        }
        .onReceive(NotificationCenter.default.publisher(for: .openNewNoteSheet)) { _ in
            model.isShowingNewNoteSheet = true
        }
        .onReceive(NotificationCenter.default.publisher(for: .openRulesSettings)) { _ in
            model.navigate(to: .rules)
        }
        .onChange(of: model.settingsRequest) { _, tab in
            guard let tab else { return }
            settingsTab = tab
            openSettings()
            model.settingsRequest = nil
        }
        .onChange(of: notificationManager.deepLinkedDocumentPath) { _, path in
            guard let path else { return }
            model.navigate(to: .docs, deepLinkPath: path)
            notificationManager.deepLinkedDocumentPath = nil
        }
        .task {
            await model.initialLoad(core: core)
        }
        .onChange(of: model.expandedFolders) { _, newValue in model.persistExpandedFolders(newValue) }
        .onChange(of: watcher.latestEvent) { _, ev in
            model.scheduleEventRefresh(ev, core: core, notificationManager: notificationManager)
        }
        .onReceive(NotificationCenter.default.publisher(for: .vaultSwitched)) { _ in
            model.reloadAfterVaultSwitch(core: core)
        }
    }

    private var workspaceContent: some View {
        VStack(spacing: 0) {
            // Banners push the page down instead of covering its section
            // bar (LAYOUT-7: controls stay reachable).
            ContentTopBannersView(model: model)
                .padding(.horizontal, SymairaSpacing.medium)
                .padding(.top, SymairaSpacing.small)
            DestinationSectionBar(model: model)
            detailContent
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("symdesk-content-\(model.destination.id)")
    }

    @ViewBuilder
    private var detailContent: some View {
        switch model.displayMode {
        case .dashboard:
            DashboardView(
                docCounts: model.docCounts,
                docTypeCounts: model.docTypeCounts,
                docTotalCount: model.docTotalCount,
                notes: model.notes,
                doctorReport: model.doctorReport,
                onNavigate: { mode in model.navigate(to: mode) },
                onOpenNote: { note in model.navigate(to: .vault, note: note) }
            )
        case .ingestQueue:
            IngestQueueView()
        case .reviewLane:
            ReviewLaneView()
        case .meetings:
            MeetingsView()
        case .discover:
            DiscoverView(onNavigateToTools: { model.navigate(to: .companionTools) })
        case .room:
            // The module owns its own state and renders an install
            // tile when symroom is absent (issue #517).
            SymroomModuleView()
        case .history:
            HistoryView(initialNotePath: model.historyInitialNotePath)
        case .trash:
            TrashView()
        case .models:
            ModelsView()
        case .duplicates:
            DuplicatesView()
        case .notebooks:
            NotebookWorkspaceView(onOpenPath: { path in model.openNotebookSourcePath(path) })
        case .graph:
            GraphView { selectedNodeID in
                model.navigateToNote(title: selectedNodeID)
            }
        case .docs:
            let statusVal = DocFilterPreset.defaults.first(where: { $0.id == model.docFilterID })?.status
            DocumentGridView(
                statusFilter: statusVal?.rawValue,
                deepLinkPath: model.deepLinkDocPath,
                tagFilter: model.tagFilter,
                onOpenInEditor: { (doc: DocumentItem) -> Void in
                    model.openDocumentInEditor(doc)
                }
            )
            .environment(\.searchAnchor, model.deepLinkAnchor)
        case .dbView:
            savedViewContent
        case .vault:
            HStack(spacing: 0) {
                NoteTreeColumn(model: model)
                    .frame(width: 260)
                Divider().overlay(SymairaTokens.line)
                VaultEditorPaneView(model: model, isBlockMode: $isBlockMode)
            }
        case .rules, .retrievalStatus, .companionTools:
            // Settings-hosted screens never render in the workspace; the
            // model redirects them to the Settings window.
            EmptyView()
        }
    }

    @ViewBuilder
    private var savedViewContent: some View {
        if let vid = model.selectedViewID {
            switch model.dbViews.first(where: { $0.id == vid })?.type {
            case "board": DbViewBoard(viewID: vid)
            case "calendar": DbViewCalendar(viewID: vid)
            case "gallery": DbViewGallery(viewID: vid)
            case "timeline": DbViewTimeline(viewID: vid)
            case "list": DbViewList(viewID: vid)
            default: DbViewTable(viewID: vid)
            }
        } else {
            SymairaEmptyState(
                icon: .bookmark,
                title: "No Saved Views",
                message: "Saved views show documents as a table, board, calendar, gallery, list or timeline."
            ) {
                Button("New View") {
                    model.editingDbView = nil
                    model.isShowingViewEditor = true
                }
                .symairaButtonStyle(.primary)
            }
            .frame(maxWidth: SymairaMetrics.readableContentWidth)
            .padding(SymairaSpacing.xLarge)
        }
    }

    // MARK: - Inspector

    @ViewBuilder
    private var inspectorContent: some View {
        if model.isShowingAIDock {
            AIDockView(context: model.aiDockContext(vaultPath: core.vaultPath))
        } else {
            VStack(alignment: .leading, spacing: SymairaSpacing.small) {
                if let note = model.selectedNote {
                    PropertiesInspector(
                        notePath: model.vaultRelativePath(note.path, vaultPath: core.vaultPath),
                        onTagClick: { tag in
                            model.navigate(to: .docs, tagFilter: tag)
                        },
                        allTags: model.tagCounts.map(\.name)
                    )
                }
                Text("BACKLINKS")
                    .symairaText(.sectionLabel)
                    .padding(.horizontal, SymairaSpacing.large)
                    .padding(.top, SymairaSpacing.medium)
                if let blErr = model.backlinksError {
                    SymairaNotice(message: blErr, tone: .warning)
                        .padding(.horizontal, SymairaSpacing.medium)
                }
                if model.backlinks.isEmpty, model.backlinksError == nil {
                    Text("No other note links here.")
                        .symairaText(.caption)
                        .padding(.horizontal, SymairaSpacing.large)
                }
                List(model.backlinks, id: \.self) { link in
                    Button {
                        model.navigateToNote(title: link)
                    } label: {
                        Label(link, symairaIcon: .link)
                            .symairaText(.body)
                    }
                    .buttonStyle(.plain)
                    .contextMenu {
                        Button("Open Note") { model.navigateToNote(title: link) }
                    }
                }
                .scrollContentBackground(.hidden)
            }
            .frame(maxHeight: .infinity, alignment: .top)
            .background(SymairaTokens.surface)
        }
    }

    // MARK: - Toolbar

    /// The window toolbar (issue #651): history navigation and the command
    /// palette next to the sidebar toggle, editor surfaces as trailing
    /// controls while a note is open. The gear comes from
    /// `SymairaWorkspaceToolbar` (SHELL-2).
    @ToolbarContentBuilder
    private var mainToolbar: some ToolbarContent {
        ToolbarItemGroup(placement: .navigation) {
            Button(action: { model.goBack() }) {
                Label { Text("Go Back") } icon: { Image(symairaIcon: .back, size: 16) }
            }
            .labelStyle(.iconOnly)
                .accessibilityLabel("Go Back")
            .disabled(!model.canGoBack)
            .help("Go back (⌘[)")
            .keyboardShortcut("[", modifiers: .command)

            Button(action: { model.goForward() }) {
                Label { Text("Go Forward") } icon: { Image(symairaIcon: .forward, size: 16) }
            }
            .labelStyle(.iconOnly)
                .accessibilityLabel("Go Forward")
            .disabled(!model.canGoForward)
            .help("Go forward (⌘])")
            .keyboardShortcut("]", modifiers: .command)

            Button(action: { model.isShowingPalette.toggle() }) {
                Label { Text("Command Palette") } icon: { Image(symairaIcon: .search, size: 16) }
            }
            .labelStyle(.iconOnly)
                .accessibilityLabel("Command Palette")
            .help("Open the command palette (⌘K)")
        }
        ToolbarItemGroup(placement: .primaryAction) {
            if model.displayMode == .vault && model.selectedNote != nil {
                Toggle(isOn: $isBlockMode) {
                    Label { Text("Block Mode") } icon: { Image(symairaIcon: .blocks, size: 16) }
                }
                .toggleStyle(.button)
                .labelStyle(.iconOnly)
                .accessibilityLabel("Block Mode")
                .help("Edit the note as blocks")

                Button(action: { model.isShowingPreview.toggle() }) {
                    Label { Text("Toggle Preview") } icon: { Image(symairaIcon: .visible, size: 16) }
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Toggle Preview")
                .help("Show or hide the Markdown preview (⌥⌘P)")

                Button(action: { model.openAIDock() }) {
                    Label { Text("AI Dock") } icon: { Image(symairaIcon: .ai, size: 16) }
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("AI Dock")
                .help("Open the AI chat dock (⌥⌘A)")

                Button(action: { model.toggleInspector() }) {
                    Label { Text("Toggle Inspector") } icon: { Image(symairaIcon: .info, size: 16) }
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Toggle Inspector")
                .help("Show or hide the properties inspector (⌥⌘I)")
            }
        }
    }
}

// MARK: - Section bar

/// Fixed-height row above a destination's content (LAYOUT-7): the section
/// switcher for multi-mode destinations plus the destination's own
/// controls. Single-mode destinations without controls reserve no row.
private struct DestinationSectionBar: View {
    @ObservedObject var model: ContentViewModel
    @EnvironmentObject var core: DeskCore
    @State private var isShowingTags = false

    private var destination: DeskDestination { model.destination }

    private var hasRow: Bool {
        destination.modes.count > 1 || destination == .notes
    }

    var body: some View {
        if hasRow {
            HStack(spacing: SymairaSpacing.medium) {
                if destination.modes.count > 1 {
                    SymairaSegmentedControl(
                        destination.title,
                        selection: Binding(
                            get: { model.displayMode },
                            set: { mode in
                                if mode == .dbView, model.selectedViewID == nil, let first = model.dbViews.first {
                                    model.navigate(to: mode, viewID: first.id)
                                } else {
                                    model.navigate(to: mode)
                                }
                            }
                        ),
                        options: destination.modes.map { (value: $0, label: $0.sectionTitle) }
                    )
                    .frame(maxWidth: CGFloat(destination.modes.count) * 150)
                }
                Spacer(minLength: SymairaSpacing.small)
                trailingControls
            }
            .frame(height: SymairaPageLayout.toolbarHeight)
            .padding(.horizontal, SymairaSpacing.xLarge)
            .padding(.top, SymairaSpacing.medium)
            .padding(.bottom, SymairaSpacing.small)
            .topoProtected()
        }
    }

    @ViewBuilder
    private var trailingControls: some View {
        switch model.displayMode {
        case .vault, .graph:
            Button {
                model.isShowingNewNoteSheet = true
            } label: {
                Label("New Note", symairaIcon: .add)
            }
            .symairaButtonStyle(.primary)
            .help("Create a note (⌘N)")
        case .docs:
            libraryControls
        case .dbView:
            savedViewControls
        default:
            EmptyView()
        }
    }

    private var libraryControls: some View {
        HStack(spacing: SymairaSpacing.small) {
            SymairaFilterMenu(
                "Filter",
                selection: Binding(
                    get: { model.docFilterID },
                    set: { model.navigate(to: .docs, docFilter: $0) }
                ),
                options: DocFilterPreset.defaults.map { preset in
                    let count = preset.displayCount(
                        statusCounts: model.docCounts,
                        typeCounts: model.docTypeCounts,
                        total: model.docTotalCount
                    )
                    return (value: preset.id, label: "\(preset.label) (\(count))")
                }
            )
            .frame(maxWidth: 220)

            Button {
                isShowingTags.toggle()
            } label: {
                Label(model.tagFilter.map { "#\($0)" } ?? "Tags", symairaIcon: .tag)
            }
            .symairaButtonStyle(.secondary)
            .help("Browse, rename and merge tags")
            .popover(isPresented: $isShowingTags, arrowEdge: .bottom) {
                TagBrowserView(
                    tags: model.tagCounts,
                    onTagClick: { tag in
                        isShowingTags = false
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
                .frame(width: 320, height: 420)
                .padding(SymairaSpacing.medium)
            }

            if model.tagFilter != nil {
                SymairaIconButton("Clear Tag Filter", icon: .close, size: 16) {
                    model.navigate(to: .docs, tagFilter: nil)
                    model.tagFilter = nil
                }
            }
        }
    }

    @ViewBuilder
    private var savedViewControls: some View {
        HStack(spacing: SymairaSpacing.small) {
            if !model.dbViews.isEmpty {
                SymairaFilterMenu(
                    "Saved View",
                    selection: Binding(
                        get: { model.selectedViewID ?? model.dbViews[0].id },
                        set: { model.navigate(to: .dbView, viewID: $0) }
                    ),
                    options: model.dbViews.map { (value: $0.id, label: $0.name) }
                )
                .frame(maxWidth: 240)

                if let current = model.dbViews.first(where: { $0.id == model.selectedViewID }) {
                    Menu {
                        Button("Edit View") {
                            model.editingDbView = current
                            model.isShowingViewEditor = true
                        }
                        Button("Delete View", role: .destructive) {
                            Task { await model.deleteView(current, core: core) }
                        }
                        .disabled(model.mutationTracker.isInFlight(model.viewDeleteActionID(current)))
                    } label: {
                        Label { Text("View Actions") } icon: { Image(symairaIcon: .more, size: 16) }
                            .labelStyle(.iconOnly)
                    }
                    .menuStyle(.borderlessButton)
                    .fixedSize()
                    .help("Edit or delete this view")
                    .accessibilityLabel("View Actions")
                    .asyncActionAlert(model.mutationTracker, id: model.viewDeleteActionID(current), title: "Couldn't Delete View") {
                        Task { await model.deleteView(current, core: core) }
                    }
                }
            }
            Button {
                model.editingDbView = nil
                model.isShowingViewEditor = true
            } label: {
                Label("New View", symairaIcon: .add)
            }
            .symairaButtonStyle(.secondary)
        }
    }
}

// MARK: - Focused destination

private struct DeskDestinationKey: FocusedValueKey {
    typealias Value = Binding<DeskDestination>
}

extension FocusedValues {
    /// The front window's destination selection, for `SymairaCommands`.
    var deskDestination: Binding<DeskDestination>? {
        get { self[DeskDestinationKey.self] }
        set { self[DeskDestinationKey.self] = newValue }
    }
}
