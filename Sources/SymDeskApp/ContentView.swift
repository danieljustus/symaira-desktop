import SwiftUI
import AppKit
import SymairaTheme
import SymDeskCore

struct ContentView: View {
    @EnvironmentObject var core: DeskCore
    @EnvironmentObject var watcher: EventWatcher
    @EnvironmentObject var notificationManager: NotificationManager

    @State private var notes: [Note] = []
    @State private var noteLookup: [String: Note] = [:]
    @State private var selectedNote: Note? = nil
    @State private var noteContent: String = ""
    @State private var loadError: String? = nil
    @State private var doctorStatus: String = "Checking..."
    @State private var doctorReport: DoctorReport? = nil
    @State private var isShowingDoctorPopover = false

    // UI State
    @State private var isShowingPalette = false
    @State private var isShowingInspector = false
    @State private var isShowingPreview = false
    @State private var isShowingAIDock = false
    @State private var isShowingNewNoteSheet = false
    @State private var newNoteTitle = ""
    @State private var backlinks: [String] = []
    @State private var backlinksError: String? = nil
    
    /// Ephemeral error banners shown at the top of the main content area.
    /// Each banner is dismissible and auto-prefixed with a category label.
    @State private var appErrors: [AppErrorMessage] = []
    
    // Folder tree state
    @State private var expandedFolders: Set<String> = []
    @State private var folderTree: [FolderNode] = []
    
    @AppStorage("isBlockMode") private var isBlockMode = false
    @AppStorage("dismissedNotificationPermissionBanner") private var dismissedNotificationPermissionBanner = false
    @AppStorage("dismissedVersionMismatchBanner") private var dismissedVersionMismatchBanner = false

    @StateObject private var mutationTracker = AsyncActionTracker<String>()
    @State private var ingestFailure: IngestFailure? = nil

    // Auto-save debounce
    @State private var saveTask: Task<Void, Never>? = nil
    @State private var eventRefreshTask: Task<Void, Never>? = nil
    @State private var keyEventMonitor: Any?

    enum DisplayMode {
        case dashboard
        case vault
        case graph
        case dbView
        case docs
        case discover
        case ingestQueue
        case reviewLane
        case rules
        case meetings
        case companionTools
        case history
        case trash
    }

    // MARK: - Navigation History

    /// Captures the full navigation context for history tracking.
    private struct NavEntry: Equatable {
        let displayMode: DisplayMode
        let notePath: String?
        let docFilterID: String
        let tagFilter: String?
        let deepLinkDocPath: String?
        let selectedViewID: String?
    }

    @State private var displayMode: DisplayMode = .dashboard
    @State private var selectedViewID: String?
    @State private var dbViews: [DbView] = []
    @State private var isShowingViewEditor = false
    @State private var editingDbView: DbView?
    @State private var docFilterID: String = "all"
    @State private var docCounts: [String: Int] = [:]
    @State private var docTotalCount: Int = 0
    @State private var deepLinkDocPath: String?

    // Navigation history stacks
    @State private var navBackStack: [NavEntry] = []
    @State private var navForwardStack: [NavEntry] = []

    // Tag browsing
    @State private var tagCounts: [TagEntry] = []
    @State private var tagFilter: String? = nil

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
                    VStack(spacing: 0) {
                        // Fixed sidebar header with title and New Note button (#293, #294a)
                        HStack {
                            Text("SymDesk")
                                .symairaText(.heading).bold()
                                .foregroundColor(SymairaTheme.textPrimary)
                            Spacer()
                            Button(action: { isShowingNewNoteSheet = true }) {
                                Label("New Note", systemImage: "plus")
                            }
                            .buttonStyle(.borderedProminent)
                            .controlSize(.small)
                            .tint(SymairaTheme.goldPrimary)
                        }
                        .padding(.horizontal, 14)
                        .padding(.vertical, 8)

                        List {
                            Section {
                                Button(action: { navigate(to: .dashboard) }) {
                                    HStack {
                                        Image(systemName: "rectangle.grid.1x2")
                                        Text("Dashboard")
                                    }
                                }
                            }

                            Section("Library") {
                                ForEach(DocFilterPreset.defaults) { preset in
                                    Button(action: {
                                        navigate(to: .docs, docFilter: preset.id)
                                    }) {
                                        HStack {
                                            Text(preset.label)
                                            Spacer()
                                            if let count = preset.status == nil ? docTotalCount : docCounts[preset.status!.rawValue] {
                                                Text("\(count)")
                                                    .symairaText(.caption)
                                                    .foregroundColor(SymairaTheme.textSecondary)
                                                    .padding(.horizontal, 6)
                                                    .padding(.vertical, 2)
                                                    .background(Color.white.opacity(0.06))
                                                    .cornerRadius(4)
                                            }
                                        }
                                    }
                                }
                            }

                            Section("Tags") {
                                TagBrowserView(tags: tagCounts) { tag in
                                    navigate(to: .docs, tagFilter: tag)
                                }
                                .frame(minHeight: 120)
                            }

                            meetingsSidebarSection

                            Section("Discover") {
                                Button(action: { navigate(to: .discover) }) {
                                    HStack {
                                        Image(systemName: "sparkles")
                                        Text("Discover")
                                    }
                                }
                                Button(action: { navigate(to: .companionTools) }) {
                                    HStack {
                                        Image(systemName: "wrench.and.screwdriver")
                                        Text("Companion Tools")
                                    }
                                }
                            }

                            Section("Inbox & Processing") {
                                Button(action: { navigate(to: .ingestQueue) }) {
                                    HStack {
                                        Image(systemName: "tray.and.arrow.down")
                                        Text("Ingest Queue")
                                    }
                                }
                                Button(action: { navigate(to: .reviewLane) }) {
                                    HStack {
                                        Image(systemName: "exclamationmark.triangle")
                                        Text("Review Lane")
                                    }
                                }
                            }

                            Section("Safety Net") {
                                Button(action: { navigate(to: .history) }) {
                                    HStack {
                                        Image(systemName: "clock.arrow.circlepath")
                                        Text("Version History")
                                    }
                                }
                                Button(action: { navigate(to: .trash) }) {
                                    HStack {
                                        Image(systemName: "trash")
                                        Text("Trash")
                                    }
                                }
                            }

                            Section("Settings") {
                                Button(action: { navigate(to: .rules) }) {
                                    HStack {
                                        Image(systemName: "gearshape")
                                        Text("Rules & Settings")
                                    }
                                }
                            }

                            Section("Views") {
                                Button("Vault") { navigate(to: .vault) }
                                Button("Graph") { navigate(to: .graph) }
                            }

                            Section("Saved Views") {
                                ForEach(dbViews) { view in
                                    Button(view.name) {
                                        navigate(to: .dbView, viewID: view.id)
                                    }
                                    .contextMenu {
                                        Button("Edit View") {
                                            editingDbView = view
                                            isShowingViewEditor = true
                                        }
                                        Button("Delete View", role: .destructive) {
                                            Task { await deleteView(view) }
                                        }
                                        .disabled(mutationTracker.isInFlight(viewDeleteActionID(view)))
                                    }
                                    .asyncActionAlert(mutationTracker, id: viewDeleteActionID(view), title: "Couldn't Delete View") {
                                        Task { await deleteView(view) }
                                    }
                                }
                                Button(action: {
                                    editingDbView = nil
                                    isShowingViewEditor = true
                                }) {
                                    HStack {
                                        Image(systemName: "plus")
                                        Text("New View")
                                    }
                                }
                            }

                            Section("Notes") {
                                if folderTree.isEmpty {
                                    Text("No notes")
                                        .foregroundColor(SymairaTheme.textMuted)
                                } else {
                                    ForEach(folderTree) { node in
                                        sidebarTreeNode(node)
                                    }
                                }
                            }
                        }
                        .scrollContentBackground(.hidden)
                        .listStyle(.sidebar)
                        .buttonStyle(.plain)
                    }
                    .frame(minWidth: 240, idealWidth: 268)
                    .background(.clear)
                } detail: {
                    SymairaScreen {
                    switch displayMode {
                    case .dashboard:
                        DashboardView(
                            docCounts: docCounts,
                            docTotalCount: docTotalCount,
                            notes: notes,
                            doctorReport: doctorReport,
                            onNavigate: { mode in navigate(to: mode) }
                        )
                    case .ingestQueue:
                        IngestQueueView()
                    case .reviewLane:
                        ReviewLaneView()
                    case .meetings:
                        MeetingsView()
                    case .rules:
                        RulesSettingsView(vaultPath: core.vaultPath)
                    case .discover:
                        DiscoverView(onNavigateToTools: { navigate(to: .companionTools) })
                    case .companionTools:
                        CompanionToolsView(
                            doctorReport: doctorReport,
                            onDoctorRefresh: { await fetchDoctor() }
                        )
                    case .history:
                        HistoryView()
                    case .trash:
                        TrashView()
                    case .graph:
                        GraphView { selectedNodeID in
                            navigateToNote(title: selectedNodeID)
                        }
                    case .docs:
                        let statusVal = DocFilterPreset.defaults.first(where: { $0.id == docFilterID })?.status
                        DocumentGridView(statusFilter: statusVal?.rawValue, deepLinkPath: deepLinkDocPath, tagFilter: tagFilter)
                    case .dbView:
                        if let vid = selectedViewID {
                            if let view = dbViews.first(where: { $0.id == vid }) {
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
                        if let note = selectedNote {
                            VStack(spacing: 0) {
                                if isConflicted(note) {
                                    HStack(spacing: 8) {
                                        Image(systemName: "exclamationmark.triangle.fill")
                                            .foregroundStyle(SymairaTheme.goldPrimary)
                                        Text("iCloud sync conflict detected")
                                            .symairaText(.caption)
                                            .foregroundColor(SymairaTheme.goldSecondary)
                                        Spacer()
                                        Button("Keep Mine") {
                                            Task { await resolveConflict(note: note, action: "keep-mine") }
                                        }
                                        .buttonStyle(.bordered)
                                        .controlSize(.small)
                                        .disabled(mutationTracker.isInFlight(conflictActionID(note: note, action: "keep-mine")))
                                        Button("Keep Theirs") {
                                            Task { await resolveConflict(note: note, action: "keep-theirs") }
                                        }
                                        .buttonStyle(.bordered)
                                        .controlSize(.small)
                                        .disabled(mutationTracker.isInFlight(conflictActionID(note: note, action: "keep-theirs")))
                                    }
                                    .padding(8)
                                    .background(SymairaTheme.goldPrimary.opacity(0.12))
                                    .cornerRadius(6)
                                    .overlay(
                                        RoundedRectangle(cornerRadius: 6)
                                            .stroke(SymairaTheme.borderGlassHover, lineWidth: 1)
                                    )
                                    .padding(.horizontal)
                                    .padding(.top, 8)
                                    .asyncActionAlert(mutationTracker, id: conflictActionID(note: note, action: "keep-mine"), title: "Couldn't Resolve Conflict") {
                                        Task { await resolveConflict(note: note, action: "keep-mine") }
                                    }
                                    .asyncActionAlert(mutationTracker, id: conflictActionID(note: note, action: "keep-theirs"), title: "Couldn't Resolve Conflict") {
                                        Task { await resolveConflict(note: note, action: "keep-theirs") }
                                    }
                                }

                                if let saveError = mutationTracker.failureMessage(for: saveActionID(note)) {
                                    HStack(spacing: 8) {
                                        Image(systemName: "exclamationmark.triangle.fill")
                                            .foregroundStyle(.red)
                                        Text("Save failed: \(saveError)")
                                            .symairaText(.caption)
                                            .foregroundColor(SymairaTheme.textSecondary)
                                        Spacer()
                                        Button("Retry") {
                                            Task { await performSave(note: note, content: noteContent) }
                                        }
                                        .buttonStyle(.bordered)
                                        .controlSize(.small)
                                        Button(action: { mutationTracker.clearFailure(for: saveActionID(note)) }) {
                                            Image(systemName: "xmark")
                                        }
                                        .buttonStyle(.plain)
                                        .foregroundStyle(SymairaTheme.textSecondary)
                                    }
                                    .padding(8)
                                    .background(Color.red.opacity(0.12))
                                    .cornerRadius(6)
                                    .padding(.horizontal)
                                    .padding(.top, 8)
                                }

                                if let loadError = loadError {
                                    HStack(spacing: 8) {
                                        Image(systemName: "exclamationmark.triangle.fill")
                                            .foregroundStyle(.red)
                                        Text(loadError)
                                            .symairaText(.caption)
                                            .foregroundColor(SymairaTheme.textSecondary)
                                        Spacer()
                                        Button("Retry") {
                                            self.loadError = nil
                                            if let note = selectedNote {
                                                Task { await loadContent(for: note) }
                                            }
                                        }
                                        .buttonStyle(.bordered)
                                        .controlSize(.small)
                                        Button(action: { self.loadError = nil }) {
                                            Image(systemName: "xmark")
                                        }
                                        .buttonStyle(.plain)
                                        .foregroundStyle(SymairaTheme.textSecondary)
                                    }
                                    .padding(8)
                                    .background(Color.red.opacity(0.12))
                                    .cornerRadius(6)
                                    .padding(.horizontal)
                                    .padding(.top, 8)
                                }

                                if loadError == nil {
                                    HStack(spacing: 0) {
                                        if isBlockMode {
                                            BlockEditorView(text: $noteContent)
                                                .padding(.top, 4)
                                        } else {
                                            MarkdownEditorView(text: $noteContent, onLinkClick: { targetTitle in
                                                navigateToNote(title: targetTitle)
                                            }, core: core, vaultRoot: core.vaultPath, onImageError: { message in
                                                appErrors.append(AppErrorMessage(
                                                    message: message,
                                                    detail: "The image was not inserted."
                                                ))
                                            })
                                        }
                                        
                                        // Dummy view to attach onChange (since we use if/else for the editor)
                                        Color.clear.frame(width: 0, height: 0)
                                            .onChange(of: noteContent) { _, newValue in
                                                debouncedSave(note: note, content: newValue)
                                            }

                                        if isShowingPreview {
                                            Divider()
                                            MarkdownPreviewView(
                                                text: noteContent,
                                                resolveNote: { target in resolveNoteContent(target) },
                                                visited: [note.title],
                                                onLinkClick: { targetTitle in
                                                    navigateToNote(title: targetTitle)
                                                }
                                            )
                                            .frame(maxWidth: .infinity)
                                        }
                                    }
                                }
                            }
                            .navigationTitle(note.title)
                            .task(id: note.id) {
                                await loadContent(for: note)
                                await loadBacklinks(for: note)
                            }
                        } else {
                            ContentUnavailableView {
                                Label("No Note Selected", systemImage: "doc.text")
                            } description: {
                                Text("Choose a note in the sidebar or open Quick Search with ⌘K.")
                            } actions: {
                                Button("Open Quick Search") { isShowingPalette = true }
                                    .buttonStyle(SymairaPrimaryButtonStyle())
                            }
                            .frame(maxWidth: 460)
                            .padding(32)
                            .symDeskLiquidGlass(cornerRadius: 20)
                        }
                    }
                    }
                }
                .navigationSplitViewStyle(.balanced)
                .frame(minWidth: 980, minHeight: 640)
                .inspector(isPresented: $isShowingInspector) {
                    if isShowingAIDock {
                        AIDockView()
                    } else {
                        VStack(alignment: .leading, spacing: 0) {
                            if let note = selectedNote {
                                PropertiesInspector(notePath: vaultRelativePath(note.path), onTagClick: { tag in
                                    navigate(to: .docs, tagFilter: tag)
                                })
                            }
                            Text("Backlinks")
                                .symairaText(.subheading)
                                .foregroundColor(SymairaTheme.goldPrimary)
                                .padding()
                            if let blErr = backlinksError {
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
                            List(backlinks, id: \.self) { link in
                                Button(link) {
                                    navigateToNote(title: link)
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
                .onDrop(of: [.fileURL], isTargeted: nil) { providers in
                    for provider in providers {
                        provider.loadItem(forTypeIdentifier: "public.file-url", options: nil) { item, error in
                            if let data = item as? Data, let url = URL(dataRepresentation: data, relativeTo: nil) {
                                Task { await ingestFile(url) }
                            }
                        }
                    }
                    return true
                }
                .alert("Couldn't Import File", isPresented: Binding(
                    get: { ingestFailure != nil },
                    set: { isPresented in if !isPresented { ingestFailure = nil } }
                )) {
                    Button("Retry") {
                        if let url = ingestFailure?.url {
                            Task { await ingestFile(url) }
                        }
                    }
                    Button("Dismiss", role: .cancel) { ingestFailure = nil }
                } message: {
                    Text(ingestFailure?.message ?? "")
                }
                .toolbar {
                    ToolbarItem(placement: .navigation) {
                        HStack(spacing: 0) {
                            Button(action: { goBack() }) {
                                Image(systemName: "chevron.left")
                            }
                            .disabled(!canGoBack)
                            .help("Go back")

                            Button(action: { goForward() }) {
                                Image(systemName: "chevron.right")
                            }
                            .disabled(!canGoForward)
                            .help("Go forward")

                            Divider()
                                .frame(height: 16)

                            Button(action: { isShowingPalette.toggle() }) {
                                Label("Command Palette", systemImage: "magnifyingglass")
                            }
                            .keyboardShortcut("k", modifiers: .command)

                            Toggle(isOn: $isBlockMode) {
                                Label("Block Mode", systemImage: "square.text.square")
                            }
                            .toggleStyle(.button)
                        }
                        .toggleStyle(.button)
                        
                        if displayMode == .vault && selectedNote != nil {
                            Button(action: { isShowingPreview.toggle() }) {
                                Label("Toggle Preview", systemImage: "sidebar.right")
                            }
                            
                            Button(action: {
                                isShowingAIDock = true
                                isShowingInspector = true
                            }) {
                                Label("AI Dock", systemImage: "sparkles")
                            }
                            
                            Button(action: {
                                isShowingAIDock = false
                                isShowingInspector.toggle()
                            }) {
                                Label("Toggle Inspector", systemImage: "info.circle")
                            }
                        }
                    }
                    ToolbarItem(placement: .status) {
                        HStack(spacing: 8) {
                            Button(action: { isShowingDoctorPopover.toggle() }) {
                                HStack(spacing: 4) {
                                    Image(systemName: (doctorReport?.overall == "ok" || doctorReport == nil) ? "checkmark.shield" : "exclamationmark.triangle")
                                        .foregroundColor(doctorReport?.overall == "ok" ? SymairaTheme.goldPrimary : SymairaTheme.goldSecondary)
                                    Text(doctorSummaryText)
                                        .symairaText(.caption)
                                        .foregroundColor(SymairaTheme.textMuted)
                                }
                            }
                            .buttonStyle(.plain)
                            .popover(isPresented: $isShowingDoctorPopover) {
                                DoctorReportPopoverView(report: doctorReport)
                            }
                            if let lastEv = watcher.latestEvent {
                                Text("Last event: \\(lastEv.event) on \\(lastEv.path)")
                                    .symairaText(.caption)
                                    .foregroundColor(SymairaTheme.textMuted)
                            }
                        }
                    }
                }
                .sheet(isPresented: $isShowingViewEditor) {
                    DbViewEditor(existing: editingDbView) {
                        Task { await fetchViews() }
                    }
                }
                .sheet(isPresented: $isShowingPalette) {
                    CommandPalette(
                        isPresented: $isShowingPalette,
                        allNotes: $notes,
                        onSelectNote: { note in
                            navigate(to: .vault, note: note)
                        },
                        onSelectSearchResult: { result in
                            // For search results, we match the path to a Note
                            if let found = notes.first(where: { $0.path == result.path }) {
                                navigate(to: .vault, note: found)
                            }
                        }
                    )
                }
                .sheet(isPresented: $isShowingNewNoteSheet) {
                    NewNoteSheet(isPresented: $isShowingNewNoteSheet, core: core)
                }
                // App-wide shortcut for Cmd-K
                .onAppear {
                    guard keyEventMonitor == nil else { return }
                    keyEventMonitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { event in
                        if event.modifierFlags.contains(.command) && event.charactersIgnoringModifiers == "k" {
                            isShowingPalette.toggle()
                            return nil
                        }
                        return event
                    }
                }
                .onDisappear {
                    saveTask?.cancel()
                    eventRefreshTask?.cancel()
                    if let keyEventMonitor {
                        NSEvent.removeMonitor(keyEventMonitor)
                        self.keyEventMonitor = nil
                    }
                }
                .onReceive(NotificationCenter.default.publisher(for: .openDiscover)) { _ in
                    navigate(to: .discover)
                }
                .onReceive(NotificationCenter.default.publisher(for: .openCommandPalette)) { _ in
                    isShowingPalette = true
                }
                .onReceive(NotificationCenter.default.publisher(for: .openNewNoteSheet)) { _ in
                    isShowingNewNoteSheet = true
                }
                .onReceive(NotificationCenter.default.publisher(for: .openRulesSettings)) { _ in
                    navigate(to: .rules)
                }
                .overlay(alignment: .top) {
                    VStack(spacing: 0) {
                        if !dismissedVersionMismatchBanner, let coreVer = core.coreVersion {
                            let appVer = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""
                            if !appVer.isEmpty && coreVer != appVer && appVer.compare(coreVer, options: .numeric) == .orderedDescending {
                                VersionMismatchBanner(appVersion: appVer, coreVersion: coreVer) {
                                    dismissedVersionMismatchBanner = true
                                }
                            }
                        }
                        if notificationManager.isDenied && !dismissedNotificationPermissionBanner {
                            NotificationDeniedBanner {
                                dismissedNotificationPermissionBanner = true
                            }
                        }
                        // Ephemeral error banners — dismissible, shown for app-level failures
                        // that would previously have been print()-only console errors.
                        ForEach(appErrors) { err in
                            AppErrorBanner(error: err) {
                                appErrors.removeAll { $0.id == err.id }
                            }
                        }
                    }
                }
                .onChange(of: notificationManager.deepLinkedDocumentPath) { _, path in
                    guard let path else { return }
                    navigate(to: .docs, deepLinkPath: path)
                    notificationManager.deepLinkedDocumentPath = nil
                }
                .task {
                    await fetchNotes()
                    await fetchViews()
                    await fetchDoctor()
                    await fetchDocCounts()
                    await fetchTagCounts()
                    // Restore expanded folder state from UserDefaults
                    if let data = UserDefaults.standard.data(forKey: "sidebarExpandedFolders"),
                       let folders = try? JSONDecoder().decode(Set<String>.self, from: data) {
                        expandedFolders = folders
                    }
                }
                .onChange(of: expandedFolders) { _, newValue in
                    // Persist expanded folder state
                    if let data = try? JSONEncoder().encode(newValue) {
                        UserDefaults.standard.set(data, forKey: "sidebarExpandedFolders")
                    }
                }
                .onChange(of: watcher.latestEvent) { _, ev in
                    scheduleEventRefresh(ev)
                }
            }
        }
    }

    /// Split out of the sidebar `List` body: inlining even one more
    /// `Section` there pushes SwiftUI's ViewBuilder type-checker over its
    /// complexity budget ("unable to type-check this expression in
    /// reasonable time").
    @ViewBuilder
    private var meetingsSidebarSection: some View {
        Section("Meetings") {
            Button(action: { navigate(to: .meetings) }) {
                HStack {
                    Image(systemName: "person.wave.2")
                    Text("Meetings")
                }
            }
        }
    }

    /// Converts an absolute note path into a vault-relative path expected by
    /// the core's property mutation commands.
    private func vaultRelativePath(_ path: String) -> String {
        guard let vault = core.vaultPath, !vault.isEmpty else { return path }
        let prefix = vault.hasSuffix("/") ? vault : vault + "/"
        if path.hasPrefix(prefix) {
            return String(path.dropFirst(prefix.count))
        }
        return path
    }

    private func fetchNotes() async {
        do {
            let loadedNotes = try await core.listFiles()
            self.notes = loadedNotes
            self.folderTree = buildFolderTree(from: loadedNotes, vaultPath: core.vaultPath)
            var lookup: [String: Note] = [:]
            lookup.reserveCapacity(loadedNotes.count * 3)
            for note in loadedNotes {
                lookup[note.title.lowercased()] = note
                let base = (note.path as NSString).lastPathComponent
                lookup[base.lowercased()] = note
                lookup[(base as NSString).deletingPathExtension.lowercased()] = note
            }
            noteLookup = lookup
        } catch {
            appErrors.append(AppErrorMessage(
                message: "Failed to load notes: \(error.localizedDescription)",
                detail: "The vault may be inaccessible or the core CLI may not be running."
            ))
        }
    }

    private func fetchViews() async {
        do {
            self.dbViews = try await core.viewsList()
        } catch {
            appErrors.append(AppErrorMessage(
                message: "Failed to load saved views: \(error.localizedDescription)"
            ))
        }
    }

    private var doctorSummaryText: String {
        guard let report = doctorReport else {
            return "Vault check unavailable — run `symdesk doctor`"
        }
        var issues: [String] = []
        if let v = report.vault, v.status != "ok" { issues.append("Vault") }
        if let s = report.sidecar, s.status != "ok" { issues.append("Sidecar") }
        if let c = report.contract, c.status != "ok" { issues.append("Contract") }
        
        let aiProvider = (report.ai?.provider ?? "Ollama").capitalized
        if issues.isEmpty && report.overall == "ok" {
            return "Vault healthy · AI: \(aiProvider)"
        } else if !issues.isEmpty {
            return "Vault: \(issues.joined(separator: ", ")) issue · AI: \(aiProvider)"
        } else {
            return "Vault warning · AI: \(aiProvider)"
        }
    }

    private func fetchDoctor() async {
        do {
            let report = try await core.getDoctorReport()
            self.doctorReport = report
            self.doctorStatus = doctorSummaryText
        } catch {
            self.doctorReport = nil
            self.doctorStatus = "Vault check unavailable"
            appErrors.append(AppErrorMessage(
                message: "Doctor check failed: \(error.localizedDescription)",
                detail: "Run `symdesk doctor` in a terminal for detailed diagnostics."
            ))
        }
    }

    private func loadContent(for note: Note) async {
        self.loadError = nil
        if core.isRemote {
            do {
                self.noteContent = try await core.docNoteContent(path: note.path)
            } catch {
                self.loadError = "Error reading file: \(error.localizedDescription)"
            }
            return
        }
        guard let path = absoluteNotePath(note.path) else {
            self.loadError = "Error reading file: no vault is configured."
            return
        }
        if let data = FileManager.default.contents(atPath: path),
           let string = String(data: data, encoding: .utf8) {
            self.noteContent = string
        } else {
            self.loadError = "Error reading file."
        }
    }

    private func loadBacklinks(for note: Note) async {
        do {
            self.backlinks = try await core.backlinks(for: note.path)
            self.backlinksError = nil
        } catch {
            self.backlinks = []
            self.backlinksError = "Could not load backlinks: \(error.localizedDescription)"
        }
    }

    private func debouncedSave(note: Note, content: String) {
        saveTask?.cancel()
        saveTask = Task {
            try? await Task.sleep(nanoseconds: 500_000_000) // 500ms
            guard !Task.isCancelled else { return }
            await performSave(note: note, content: content)
        }
    }

    private func saveActionID(_ note: Note) -> String {
        "save:\(note.path)"
    }

    private func performSave(note: Note, content: String) async {
        await mutationTracker.run(saveActionID(note)) {
            if core.isRemote {
                try await core.saveNoteContent(path: note.path, content: content)
            } else if let path = absoluteNotePath(note.path) {
                try await core.saveNoteContent(path: path, content: content)
            }
        }
    }

    private func viewDeleteActionID(_ view: DbView) -> String {
        "view-delete:\(view.id)"
    }

    private func deleteView(_ view: DbView) async {
        let succeeded = await mutationTracker.run(viewDeleteActionID(view)) {
            try await core.viewsDelete(id: view.id)
        }
        guard succeeded else { return }
        if selectedViewID == view.id { selectedViewID = nil }
        await fetchViews()
    }

    private func conflictActionID(note: Note, action: String) -> String {
        "conflict:\(note.path):\(action)"
    }

    private func resolveConflict(note: Note, action: String) async {
        let succeeded = await mutationTracker.run(conflictActionID(note: note, action: action)) {
            try await core.resolveConflict(path: note.path, action: action)
        }
        guard succeeded else { return }
        if selectedNote?.path == note.path {
            self.selectedNote = nil
        }
        await fetchNotes()
    }

    private func ingestActionID(_ url: URL) -> String {
        "ingest:\(url.path)"
    }

    private func ingestFile(_ url: URL) async {
        let actionID = ingestActionID(url)
        await mutationTracker.run(actionID) {
            _ = try await core.ingest(fileURL: url)
            // fetchNotes() is called by the watcher automatically, so we don't need to manually refresh
        }
        if let message = mutationTracker.failureMessage(for: actionID) {
            ingestFailure = IngestFailure(url: url, message: message)
            mutationTracker.clearFailure(for: actionID)
        } else if ingestFailure?.url == url {
            ingestFailure = nil
        }
    }

    /// Resolves a transclusion target ("Note Title" or relative path without
    /// extension) to the note's raw Markdown for the preview embeds.
    private func resolveNoteContent(_ target: String) -> String? {
		guard !core.isRemote else { return nil }
        guard let found = noteLookup[target.lowercased()],
              let path = absoluteNotePath(found.path),
              let data = FileManager.default.contents(atPath: path) else { return nil }
        return String(data: data, encoding: .utf8)
    }

    private func absoluteNotePath(_ path: String) -> String? {
        DocumentPreviewResolver.noteURL(documentPath: path, vaultPath: core.vaultPath)?.path
    }

    // MARK: - Navigation History Helpers

    private var canGoBack: Bool { !navBackStack.isEmpty }
    private var canGoForward: Bool { !navForwardStack.isEmpty }

    /// Snapshots the current navigation state.
    private func makeNavEntry() -> NavEntry {
        NavEntry(
            displayMode: displayMode,
            notePath: selectedNote?.path,
            docFilterID: docFilterID,
            tagFilter: tagFilter,
            deepLinkDocPath: deepLinkDocPath,
            selectedViewID: selectedViewID
        )
    }

    /// Restores a navigation state, re-resolving the note from the current
    /// notes list since Note is a value type (new instance after refresh).
    private func applyNavEntry(_ entry: NavEntry) {
        displayMode = entry.displayMode
        selectedNote = entry.notePath.flatMap { path in notes.first(where: { $0.path == path }) }
        docFilterID = entry.docFilterID
        tagFilter = entry.tagFilter
        deepLinkDocPath = entry.deepLinkDocPath
        selectedViewID = entry.selectedViewID
    }

    /// Navigate to a new destination, pushing the current state onto the
    /// back stack so the user can return with the back button.
    private func navigate(
        to mode: DisplayMode,
        note: Note? = nil,
        docFilter: String? = nil,
        tagFilter: String? = nil,
        deepLinkPath: String? = nil,
        viewID: String? = nil
    ) {
        navBackStack.append(makeNavEntry())
        navForwardStack.removeAll()
        displayMode = mode
        if let note = note { selectedNote = note }
        if let docFilter = docFilter { docFilterID = docFilter }
        if let tagFilter = tagFilter { self.tagFilter = tagFilter }
        if let deepLinkPath = deepLinkPath { deepLinkDocPath = deepLinkPath }
        if let viewID = viewID { selectedViewID = viewID }
    }

    /// Go back one step in navigation history.
    private func goBack() {
        guard let entry = navBackStack.popLast() else { return }
        navForwardStack.append(makeNavEntry())
        applyNavEntry(entry)
    }

    /// Go forward one step in navigation history.
    private func goForward() {
        guard let entry = navForwardStack.popLast() else { return }
        navBackStack.append(makeNavEntry())
        applyNavEntry(entry)
    }

    private func navigateToNote(title: String) {
        if let found = noteLookup[title.lowercased()] {
            navigate(to: .vault, note: found)
        }
    }

    /// File watchers commonly emit several events for one atomic save. Wait
    /// for the burst to settle before refreshing lists, counts, and reminders.
    private func scheduleEventRefresh(_ event: VaultEvent?) {
        eventRefreshTask?.cancel()
        eventRefreshTask = Task {
            try? await Task.sleep(nanoseconds: 250_000_000)
            guard !Task.isCancelled else { return }
            await fetchNotes()
            await fetchDocCounts()
            await fetchTagCounts()
            if let selected = selectedNote, event?.path == selected.path {
                await loadContent(for: selected)
            }
            await notificationManager.refreshNotifications(with: core)
        }
    }

    private func fetchDocCounts() async {
        do {
            let all = try await core.docsList()
            docTotalCount = all.count
            var counts: [String: Int] = [:]
            for doc in all {
                let key = doc.status.isEmpty ? "unset" : doc.status
                counts[key, default: 0] += 1
            }
            docCounts = counts
        } catch {
            appErrors.append(AppErrorMessage(
                message: "Failed to load document counts: \(error.localizedDescription)"
            ))
        }
    }

    /// Scan vault files and aggregate tag counts for the tag browser.
    private func fetchTagCounts() async {
        guard !notes.isEmpty else { return }
        do {
            tagCounts = try await TagStore.aggregate(from: notes, vaultPath: core.vaultPath, isRemote: core.isRemote, core: core)
        } catch {
            print("fetchTagCounts failed: \(error)")
        }
    }

    private func isConflicted(_ note: Note) -> Bool {
        return note.path.contains(" 2.md") || note.path.contains("conflicted copy")
    }

    // MARK: - Folder Tree Support

    /// Builds a folder tree from the flat list of notes.
    private func buildFolderTree(from notes: [Note], vaultPath: String?) -> [FolderNode] {
        guard let vaultPath = vaultPath, !vaultPath.isEmpty else {
            return notes.map { FolderNode(id: $0.path, name: $0.title, isFolder: false, note: $0, children: [], containingFolder: nil) }
        }

        let normalizedVault = vaultPath.hasSuffix("/") ? vaultPath : vaultPath + "/"

        // Count duplicate titles so identically-named notes show their folder context
        let titleCounts = Dictionary(grouping: notes, by: \.title).mapValues(\.count)

        // Build a trie from relative note paths
        class TrieNode {
            var name: String
            var notes: [Note] = []
            var children: [String: TrieNode] = [:]
            init(name: String) { self.name = name }
        }

        let root = TrieNode(name: "")

        for note in notes {
            guard note.path.hasPrefix(normalizedVault) else { continue }
            let relPath = String(note.path.dropFirst(normalizedVault.count))
            var components = relPath.split(separator: "/").map(String.init)
            guard !components.isEmpty else { continue }
            components.removeLast() // strip the filename

            var current = root
            for component in components {
                if current.children[component] == nil {
                    current.children[component] = TrieNode(name: component)
                }
                current = current.children[component]!
            }
            current.notes.append(note)
        }

        func convert(_ node: TrieNode) -> [FolderNode] {
            var result: [FolderNode] = []

            // Folders first, sorted alphabetically
            for key in node.children.keys.sorted(by: { $0.localizedCaseInsensitiveCompare($1) == .orderedAscending }) {
                let child = node.children[key]!
                let subChildren = convert(child)
                result.append(FolderNode(
                    id: key,
                    name: key,
                    isFolder: true,
                    note: nil,
                    children: subChildren,
                    containingFolder: nil
                ))
            }

            // Notes, sorted alphabetically by title
            for note in node.notes.sorted(by: { $0.title.localizedCaseInsensitiveCompare($1.title) == .orderedAscending }) {
                let hasDuplicates = (titleCounts[note.title] ?? 0) > 1
                let containingFolder: String?
                if hasDuplicates, !node.name.isEmpty {
                    containingFolder = node.name
                } else if hasDuplicates {
                    containingFolder = "Vault root"
                } else {
                    containingFolder = nil
                }
                result.append(FolderNode(
                    id: note.path,
                    name: note.title,
                    isFolder: false,
                    note: note,
                    children: [],
                    containingFolder: containingFolder
                ))
            }

            return result
        }

        return convert(root)
    }

    /// Recursively renders a folder tree node (folder or note leaf) in the sidebar.
    @ViewBuilder
    private func sidebarTreeNode(_ node: FolderNode) -> some View {
        if node.isFolder {
            DisclosureGroup(
                isExpanded: Binding(
                    get: { expandedFolders.contains(node.id) },
                    set: { isExpanded in
                        if isExpanded {
                            expandedFolders.insert(node.id)
                        } else {
                            expandedFolders.remove(node.id)
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
                    navigate(to: .vault, note: note)
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
        }
    }
}

// MARK: - Folder Node Model

/// A node in the sidebar folder tree — either a folder (with children)
/// or a note leaf.
private struct FolderNode: Identifiable, Hashable {
    let id: String
    let name: String
    let isFolder: Bool
    let note: Note?
    let children: [FolderNode]
    /// Set for leaf nodes that share their title with another note.
    /// Contains the parent folder name to help disambiguate.
    let containingFolder: String?

    func hash(into hasher: inout Hasher) {
        hasher.combine(id)
    }

    static func == (lhs: FolderNode, rhs: FolderNode) -> Bool {
        lhs.id == rhs.id
    }
}

// MARK: - Doctor Report Popover View

private struct DoctorReportPopoverView: View {
    let report: DoctorReport?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("System Diagnostics")
                .symairaText(.subheading)
                .foregroundColor(SymairaTheme.textPrimary)

            if let report {
                VStack(alignment: .leading, spacing: 8) {
                    statusRow(label: "Vault", status: report.vault?.status, detail: report.vault?.message ?? report.vault?.path)
                    statusRow(label: "Sidecar Index", status: report.sidecar?.status, detail: report.sidecar?.message)
                    statusRow(label: "Contract & ASN", status: report.contract?.status, detail: report.contract?.message ?? (report.contract?.filesFound.map { "\($0) files scanned" }))
                    if let ai = report.ai {
                        HStack {
                            Text("AI Provider:").symairaText(.caption).fontWeight(.medium).foregroundColor(SymairaTheme.textSecondary)
                            Text("\(ai.provider ?? "Ollama") \(ai.model ?? "")").symairaText(.caption).foregroundColor(SymairaTheme.textPrimary)
                        }
                    }
                    Divider()
                    Text("Tools:").symairaText(.caption).fontWeight(.semibold).foregroundColor(SymairaTheme.textSecondary)
                    VStack(alignment: .leading, spacing: 4) {
                        toolRow("symseek", report: report)
                        toolRow("symmemory", report: report)
                        toolRow("symingest", report: report)
                        toolRow("symfetch", report: report)
                        toolRow("symvault", report: report)
                        toolRow("symmeet", report: report)
                    }
                }
            } else {
                Text("Doctor report unavailable.")
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textSecondary)
            }
        }
        .padding(16)
        .frame(width: 280)
    }

    private func statusRow(label: String, status: String?, detail: String?) -> some View {
        HStack(alignment: .top) {
            Image(systemName: status == "ok" ? "checkmark.circle.fill" : "exclamationmark.triangle.fill")
                .foregroundColor(status == "ok" ? .green : .orange)
                .symairaText(.caption)
            VStack(alignment: .leading, spacing: 2) {
                Text("\(label): \(status ?? "unknown")")
                    .symairaText(.caption).fontWeight(.medium)
                    .foregroundColor(SymairaTheme.textPrimary)
                if let detail, !detail.isEmpty {
                    Text(detail)
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textSecondary)
                }
            }
        }
    }

    private func toolRow(_ tool: String, report: DoctorReport) -> some View {
        let isAvail = report.tools.isAvailable(tool)
        let version = report.versions?[tool] ?? ""
        return HStack {
            Image(systemName: isAvail ? "checkmark" : "xmark")
                .symairaText(.caption)
                .foregroundColor(isAvail ? .green : .secondary)
            Text(tool)
                .symairaText(.caption)
                .foregroundColor(SymairaTheme.textPrimary)
            Spacer()
            if !version.isEmpty {
                Text("v\(version)")
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
            }
        }
    }
}

// MARK: - Ingest Failure

private struct IngestFailure: Equatable {
    let url: URL
    let message: String
}

// MARK: - New Note Sheet

private struct NewNoteSheet: View {
    @Binding var isPresented: Bool
    let core: DeskCore
    
    @State private var title = ""
    @State private var isCreating = false
    @State private var errorMessage: String?
    @FocusState private var isTitleFocused: Bool
    
    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Image(systemName: "doc.badge.plus")
                    .symairaText(.title)
                    .foregroundColor(SymairaTheme.goldPrimary)
                TextField("Note title", text: $title)
                    .textFieldStyle(.plain)
                    .symairaText(.title)
                    .foregroundColor(SymairaTheme.textPrimary)
                    .focused($isTitleFocused)
                    .onSubmit { createNote() }
                    .disabled(isCreating)
                if !title.isEmpty {
                    Button {
                        title = ""
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(SymairaTheme.textMuted)
                    }
                    .buttonStyle(.plain)
                    .help("Clear")
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
            .padding(16)
            
            HStack {
                if isCreating {
                    ProgressView()
                        .controlSize(.small)
                        .padding(.horizontal, 8)
                }
                if let err = errorMessage {
                    Text(err)
                        .symairaText(.caption)
                        .foregroundColor(.red)
                }
                Spacer()
                Button("Cancel") { isPresented = false }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                    .disabled(isCreating)
                Button("Create") { createNote() }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .tint(SymairaTheme.goldPrimary)
                    .disabled(title.isEmpty || isCreating)
            }
            .padding(.horizontal, 24)
            .padding(.bottom, 16)
        }
        .background { SymairaScreen { Color.clear } }
        .frame(width: 440, height: 160)
        .onAppear { isTitleFocused = true }
    }
    
    private func createNote() {
        guard !title.trimmingCharacters(in: .whitespaces).isEmpty else { return }
        isCreating = true
        errorMessage = nil
        Task {
            do {
                let _ = try await core.noteNew(title: title.trimmingCharacters(in: .whitespaces))
                await MainActor.run {
                    isPresented = false
                    isCreating = false
                }
            } catch {
                await MainActor.run {
                    errorMessage = "Could not create note: \(error.localizedDescription)"
                    isCreating = false
                }
            }
        }
    }
}

// MARK: - Notification Permission Denied Banner

private struct NotificationDeniedBanner: View {
    let dismiss: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "bell.slash.fill")
                .foregroundStyle(SymairaTheme.goldPrimary)
            VStack(alignment: .leading, spacing: 1) {
                Text("Notifications are off")
                    .symairaText(.caption).fontWeight(.semibold)
                    .foregroundStyle(SymairaTheme.textPrimary)
                Text("Enable them in System Settings to receive review reminders.")
                    .symairaText(.caption)
                    .foregroundStyle(SymairaTheme.textSecondary)
            }
            Spacer(minLength: 12)
            Button("Open Settings") {
                if let url = URL(string: "x-apple.systempreferences:com.apple.preference.notifications?PrivacyNotificationCenter") {
                    NSWorkspace.shared.open(url)
                }
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
            Button(action: dismiss) {
                Image(systemName: "xmark")
            }
            .buttonStyle(.plain)
            .foregroundStyle(SymairaTheme.textSecondary)
            .help("Dismiss notification reminder")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
        .padding(.horizontal, 16)
        .padding(.top, 10)
        .accessibilityElement(children: .contain)
    }
}

// MARK: - App Error Banner

/// A short-lived, dismissible error banner for app-level failures that would
/// previously have been visible only in the Xcode console (print() calls).
private struct AppErrorMessage: Identifiable, Equatable {
    let id = UUID()
    let message: String
    var detail: String? = nil

    static func == (lhs: AppErrorMessage, rhs: AppErrorMessage) -> Bool {
        lhs.id == rhs.id
    }
}

private struct AppErrorBanner: View {
    let error: AppErrorMessage
    let dismiss: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(SymairaTheme.goldPrimary)
            VStack(alignment: .leading, spacing: 1) {
                Text(error.message)
                    .symairaText(.caption).fontWeight(.semibold)
                    .foregroundStyle(SymairaTheme.textPrimary)
                if let detail = error.detail {
                    Text(detail)
                        .symairaText(.caption)
                        .foregroundStyle(SymairaTheme.textSecondary)
                }
            }
            Spacer(minLength: 12)
            Button(action: dismiss) {
                Image(systemName: "xmark")
            }
            .buttonStyle(.plain)
            .foregroundStyle(SymairaTheme.textSecondary)
            .help("Dismiss error")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
        .padding(.horizontal, 16)
        .padding(.top, 10)
        .accessibilityElement(children: .contain)
        .transition(.move(edge: .top).combined(with: .opacity))
    }
}

// MARK: - Version Mismatch Banner

/// Persistent, dismissible banner shown when the installed `symdesk` CLI is
/// older than the app version. An older CLI silently applies older vault rules,
/// so the mismatch is surfaced rather than ignored (issue #246).
private struct VersionMismatchBanner: View {
    let appVersion: String
    let coreVersion: String
    let dismiss: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(SymairaTheme.goldPrimary)
            VStack(alignment: .leading, spacing: 1) {
                Text("CLI version mismatch")
                    .symairaText(.caption).fontWeight(.semibold)
                    .foregroundStyle(SymairaTheme.textPrimary)
                Text("App v\(appVersion) is driving CLI v\(coreVersion). Run `brew upgrade symdesk` to update.")
                    .symairaText(.caption)
                    .foregroundStyle(SymairaTheme.textSecondary)
            }
            Spacer(minLength: 12)
            Button(action: dismiss) {
                Image(systemName: "xmark")
            }
            .buttonStyle(.plain)
            .foregroundStyle(SymairaTheme.textSecondary)
            .help("Dismiss version mismatch warning")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
        .padding(.horizontal, 16)
        .padding(.top, 10)
        .accessibilityElement(children: .contain)
    }
}
