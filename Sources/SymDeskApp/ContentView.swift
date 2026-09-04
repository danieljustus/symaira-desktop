import SwiftUI
import AppKit
import SymairaTheme
import SymDeskCore
import SymroomFeature

struct ContentView: View {
    typealias DisplayMode = ContentDisplayMode

    @EnvironmentObject var core: DeskCore
    @EnvironmentObject var watcher: EventWatcher
    @EnvironmentObject var notificationManager: NotificationManager

    @StateObject private var model = ContentViewModel()
    @AppStorage("isBlockMode") private var isBlockMode = false

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
        case .rules:
            RulesSettingsView()
        case .discover:
            DiscoverView(onNavigateToTools: { model.navigate(to: .companionTools) })
        case .retrievalStatus:
            RetrievalStatusView()
        case .room:
            // The module owns its own state and renders an install
            // tile when symroom is absent (issue #517).
            SymroomModuleView()
        case .companionTools:
            CompanionToolsView(
                doctorReport: model.doctorReport,
                onDoctorRefresh: { await model.fetchDoctor(core: core) }
            )
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
            if let vid = model.selectedViewID {
                if let view = model.dbViews.first(where: { $0.id == vid }) {
                    if view.type == "board" {
                        DbViewBoard(viewID: vid)
                    } else if view.type == "calendar" {
                        DbViewCalendar(viewID: vid)
                    } else if view.type == "gallery" {
                        DbViewGallery(viewID: vid)
                    } else if view.type == "timeline" {
                        DbViewTimeline(viewID: vid)
                    } else if view.type == "list" {
                        DbViewList(viewID: vid)
                    } else {
                        DbViewTable(viewID: vid)
                    }
                } else {
                    DbViewTable(viewID: vid)
                }
            } else {
                Text("Select a view")
                    .foregroundColor(SymairaTheme.textMuted)
            }
        case .vault:
            VaultEditorPaneView(model: model, isBlockMode: $isBlockMode)
        }
    }

    var body: some View {
        Group {
            if !core.isReady {
                if let err = core.errorMessage {
                    SymairaScreen {
                        VStack(spacing: 12) {
                            Image(systemName: "exclamationmark.triangle")
                                .symairaText(.title)
                                .foregroundColor(SymairaTheme.goldPrimary)
                            Text("Error")
                                .symairaText(.title).bold()
                                .foregroundColor(SymairaTheme.textPrimary)
                            Text(err)
                                .foregroundColor(SymairaTheme.textSecondary)
                            Text(ServerConnectionConfig.hasConnection ? "Check the server URL, network and access token, then restart SymDesk." : "Run `brew install danieljustus/tap/symdesk` to install the core CLI.")
                                .foregroundColor(SymairaTheme.textMuted)
                                .padding(.top)
                        }
                        .padding(28)
                        .glassmorphicPanel()
                        .padding(40)
                    }
                } else {
                    SymairaScreen {
                        ProgressView("Connecting to SymDesk Core...")
                            .tint(SymairaTheme.goldPrimary)
                            .foregroundColor(SymairaTheme.textSecondary)
                    }
                }
            } else {
                NavigationSplitView {
                    ContentSidebarView(model: model)
                } detail: {
                    SymairaScreen {
                        detailContent
                    }
                }
                .navigationSplitViewStyle(.balanced)
                .frame(minWidth: 980, minHeight: 640)
                .inspector(isPresented: $model.isShowingInspector) {
                    if model.isShowingAIDock {
                        AIDockView(context: model.aiDockContext(vaultPath: core.vaultPath))
                    } else {
                        VStack(alignment: .leading, spacing: 0) {
                            if let note = model.selectedNote {
                                PropertiesInspector(
                                    notePath: model.vaultRelativePath(note.path, vaultPath: core.vaultPath),
                                    onTagClick: { tag in
                                        model.navigate(to: .docs, tagFilter: tag)
                                    },
                                    allTags: model.tagCounts.map(\.name)
                                )
                            }
                            Text("Backlinks")
                                .symairaText(.subheading)
                                .foregroundColor(SymairaTheme.goldPrimary)
                                .padding()
                            if let blErr = model.backlinksError {
                                HStack(spacing: 6) {
                                    Image(systemName: "exclamationmark.triangle.fill")
                                        .foregroundStyle(.orange)
                                        .symairaText(.caption)
                                    Text(blErr)
                                        .symairaText(.caption)
                                        .foregroundColor(SymairaTheme.textSecondary)
                                    Spacer()
                                }
                                .padding(.horizontal)
                                .padding(.bottom, 8)
                            }
                            List(model.backlinks, id: \.self) { link in
                                Button(link) {
                                    model.navigateToNote(title: link)
                                }
                                .buttonStyle(PlainButtonStyle())
                                .foregroundColor(SymairaTheme.textSecondary)
                            }
                            .scrollContentBackground(.hidden)
                        }
                        .frame(minWidth: 200)
                        .background(SymairaTheme.bgDarker)
                    }
                }
                .onDrop(of: [.fileURL], isTargeted: nil, perform: { providers in model.handleDrop(providers, core: core) })
                .toolbar {
                    mainToolbar
                }
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
                .overlay(alignment: .top) {
                    ContentTopBannersView(model: model)
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
        }
    }

    /// The window toolbar (issue #651): extracted from `body` so the giant
    /// split-view body stays within the type-checker's budget.
    @ToolbarContentBuilder
    private var mainToolbar: some ToolbarContent {
        ToolbarItem(placement: .navigation) {
            HStack(spacing: 0) {
                Button(action: { model.goBack() }) {
                    Image(systemName: "chevron.left")
                }
                .disabled(!model.canGoBack)
                .help("Go back")

                Button(action: { model.goForward() }) {
                    Image(systemName: "chevron.right")
                }
                .disabled(!model.canGoForward)
                .help("Go forward")

                Divider()
                    .frame(height: 16)

                Button(action: { model.isShowingPalette.toggle() }) {
                    Label("Command Palette", systemImage: "magnifyingglass")
                }
                .keyboardShortcut("k", modifiers: .command)

                Toggle(isOn: $isBlockMode) {
                    Label("Block Mode", systemImage: "square.text.square")
                }
                .toggleStyle(.button)
            }
            .toggleStyle(.button)
        }
        // Each editor surface gets its own ToolbarItem — they were
        // previously declared as a second top-level view inside the
        // navigation item and never rendered (issue #651).
        ToolbarItem(placement: .primaryAction) {
            if model.displayMode == .vault && model.selectedNote != nil {
                Button(action: { model.isShowingPreview.toggle() }) {
                    Label("Toggle Preview", systemImage: "sidebar.right")
                }
                .help("Show or hide the Markdown preview (⌥⌘P)")
            }
        }
        ToolbarItem(placement: .primaryAction) {
            if model.displayMode == .vault && model.selectedNote != nil {
                Button(action: { model.openAIDock() }) {
                    Label("AI Dock", systemImage: "sparkles")
                }
                .help("Open the AI chat dock (⌥⌘A)")
            }
        }
        ToolbarItem(placement: .primaryAction) {
            if model.displayMode == .vault && model.selectedNote != nil {
                Button(action: { model.toggleInspector() }) {
                    Label("Toggle Inspector", systemImage: "info.circle")
                }
                .help("Show or hide the properties inspector (⌥⌘I)")
            }
        }
        ToolbarItem(placement: .status) {
            HStack(spacing: 8) {
                Button(action: { model.isShowingDoctorPopover.toggle() }) {
                    HStack(spacing: 4) {
                        Image(systemName: (model.doctorReport?.overall == "ok" || model.doctorReport == nil) ? "checkmark.shield" : "exclamationmark.triangle")
                            .foregroundColor(model.doctorReport?.overall == "ok" ? SymairaTheme.goldPrimary : SymairaTheme.goldSecondary)
                        Text(model.doctorSummaryText)
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textMuted)
                    }
                }
                .buttonStyle(.plain)
                .popover(isPresented: $model.isShowingDoctorPopover) {
                    DoctorReportPopoverView(report: model.doctorReport)
                }
                if let lastEv = watcher.latestEvent {
                    Text("Last event: \(lastEv.event) on \(model.vaultRelativePath(lastEv.path, vaultPath: core.vaultPath))")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                }
            }
        }
    }
}
