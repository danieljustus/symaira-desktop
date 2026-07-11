import SwiftUI
import AppKit
import SymairaTheme
import SymDeskCore

struct ContentView: View {
    @EnvironmentObject var core: DeskCore
    @EnvironmentObject var watcher: EventWatcher
    @EnvironmentObject var notificationManager: NotificationManager

    @State private var notes: [Note] = []
    @State private var selectedNote: Note? = nil
    @State private var noteContent: String = ""
    @State private var doctorStatus: String = "Checking..."

    // UI State
    @State private var isShowingPalette = false
    @State private var isShowingInspector = false
    @State private var isShowingPreview = false
    @State private var isShowingAIDock = false
    @State private var backlinks: [String] = []
    
    @AppStorage("isBlockMode") private var isBlockMode = false

    // Auto-save debounce
    @State private var saveTask: Task<Void, Never>? = nil

    enum DisplayMode {
        case vault
        case graph
        case dbView
        case docs
        case discover
        case ingestQueue
        case reviewLane
    }

    @State private var displayMode: DisplayMode = .vault
    @State private var selectedViewID: String?
    @State private var dbViews: [DbView] = []
    @State private var isShowingViewEditor = false
    @State private var editingDbView: DbView?
    @State private var docFilterID: String = "all"
    @State private var docCounts: [String: Int] = [:]
    @State private var docTotalCount: Int = 0
    @State private var deepLinkDocPath: String?

    var body: some View {
        Group {
            if !core.isReady {
                if let err = core.errorMessage {
                    SymairaScreen {
                        VStack(spacing: 12) {
                            Image(systemName: "exclamationmark.triangle")
                                .font(.system(size: 40))
                                .foregroundColor(SymairaTheme.goldPrimary)
                            Text("Error")
                                .font(.title.bold())
                                .foregroundColor(SymairaTheme.textPrimary)
                            Text(err)
                                .foregroundColor(SymairaTheme.textSecondary)
                            Text("Run `brew install danieljustus/tap/symdesk` to install the core CLI.")
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
                    List {
                        Section("Library") {
                            ForEach(DocFilterPreset.defaults) { preset in
                                Button(action: {
                                    docFilterID = preset.id
                                    displayMode = .docs
                                }) {
                                    HStack {
                                        Text(preset.label)
                                        Spacer()
                                        if let count = preset.status == nil ? docTotalCount : docCounts[preset.status!.rawValue] {
                                            Text("\(count)")
                                                .font(.caption)
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

                        Section("Discover") {
                            Button(action: { displayMode = .discover }) {
                                HStack {
                                    Image(systemName: "sparkles")
                                    Text("Discover")
                                }
                            }
                        }

                        Section("Inbox & Processing") {
                            Button(action: { displayMode = .ingestQueue }) {
                                HStack {
                                    Image(systemName: "tray.and.arrow.down")
                                    Text("Ingest Queue")
                                }
                            }
                            Button(action: { displayMode = .reviewLane }) {
                                HStack {
                                    Image(systemName: "exclamationmark.triangle")
                                    Text("Review Lane")
                                }
                            }
                        }

                        Section("Views") {
                            Button("Vault") { displayMode = .vault }
                            Button("Graph") { displayMode = .graph }
                        }

                        Section("Saved Views") {
                            ForEach(dbViews) { view in
                                Button(view.name) {
                                    selectedViewID = view.id
                                    displayMode = .dbView
                                }
                                .contextMenu {
                                    Button("Edit View") {
                                        editingDbView = view
                                        isShowingViewEditor = true
                                    }
                                    Button("Delete View", role: .destructive) {
                                        Task {
                                            try? await core.viewsDelete(id: view.id)
                                            if selectedViewID == view.id { selectedViewID = nil }
                                            await fetchViews()
                                        }
                                    }
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
                            ForEach(notes) { note in
                                Button(note.title) {
                                    self.selectedNote = note
                                    self.displayMode = .vault
                                }
                            }
                        }
                    }
                    .scrollContentBackground(.hidden)
                    .background(SymairaTheme.bgDarker)
                    .navigationTitle("SymDesk")
                } detail: {
                    SymairaScreen {
                    switch displayMode {
                    case .ingestQueue:
                        IngestQueueView()
                    case .reviewLane:
                        ReviewLaneView()
                    case .discover:
                        DiscoverView()
                    case .graph:
                        GraphView { selectedNodeID in
                            navigateToNote(title: selectedNodeID)
                            displayMode = .vault
                        }
                    case .docs:
                        let statusVal = DocFilterPreset.defaults.first(where: { $0.id == docFilterID })?.status
                        DocumentGridView(statusFilter: statusVal?.rawValue, deepLinkPath: deepLinkDocPath)
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
                                    HStack {
                                        Text("⚠️ iCloud Sync Conflict detected")
                                            .font(.caption)
                                            .foregroundColor(SymairaTheme.goldSecondary)
                                        Spacer()
                                        Button("Keep Mine") {
                                            Task {
                                                try? await core.resolveConflict(path: note.path, action: "keep-mine")
                                                self.selectedNote = nil
                                                await fetchNotes()
                                            }
                                        }
                                        .buttonStyle(.bordered)
                                        .controlSize(.small)
                                        Button("Keep Theirs") {
                                            Task {
                                                try? await core.resolveConflict(path: note.path, action: "keep-theirs")
                                                self.selectedNote = nil
                                                await fetchNotes()
                                            }
                                        }
                                        .buttonStyle(.bordered)
                                        .controlSize(.small)
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
                                }

                                HStack(spacing: 0) {
                                    if isBlockMode {
                                        BlockEditorView(text: $noteContent)
                                            .padding(.top, 4)
                                    } else {
                                        MarkdownEditorView(text: $noteContent, onLinkClick: { targetTitle in
                                            navigateToNote(title: targetTitle)
                                        }, core: core)
                                    }
                                    
                                    // Dummy view to attach onChange (since we use if/else for the editor)
                                    Color.clear.frame(width: 0, height: 0)
                                        .onChange(of: noteContent) { newValue in
                                            debouncedSave(note: note, content: newValue)
                                        }

                                    if isShowingPreview {
                                        Divider()
                                        ScrollView {
                                            Text(LocalizedStringKey(noteContent))
                                                .padding()
                                                .frame(maxWidth: .infinity, alignment: .leading)
                                        }
                                        .frame(maxWidth: .infinity)
                                    }
                                }
                            }
                            .navigationTitle(note.title)
                            .toolbar {
                                ToolbarItem {
                                    Button(action: { isShowingPreview.toggle() }) {
                                        Label("Toggle Preview", systemImage: "sidebar.right")
                                    }
                                }
                                ToolbarItem {
                                    Button(action: {
                                        isShowingAIDock = true
                                        isShowingInspector = true
                                    }) {
                                        Label("AI Dock", systemImage: "sparkles")
                                    }
                                }
                                ToolbarItem {
                                    Button(action: {
                                        isShowingAIDock = false
                                        isShowingInspector.toggle()
                                    }) {
                                        Label("Toggle Inspector", systemImage: "info.circle")
                                    }
                                }
                            }
                            .task(id: note.id) {
                                await loadContent(for: note)
                                await loadBacklinks(for: note)
                            }
                        } else {
                            Text("Select a note or press Cmd-K")
                                .foregroundColor(SymairaTheme.textMuted)
                        }
                    }
                    }
                }
                .inspector(isPresented: $isShowingInspector) {
                    if isShowingAIDock {
                        AIDockView()
                    } else {
                        VStack(alignment: .leading, spacing: 0) {
                            if let note = selectedNote {
                                PropertiesInspector(notePath: vaultRelativePath(note.path))
                            }
                            Text("Backlinks")
                                .font(.headline)
                                .foregroundColor(SymairaTheme.goldPrimary)
                                .padding()
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
                                Task {
                                    do {
                                        let _ = try await core.ingest(fileURL: url)
                                        // fetchNotes() is called by the watcher automatically, so we don't need to manually refresh
                                    } catch {
                                        print("Ingest failed: \(error)")
                                    }
                                }
                            }
                        }
                    }
                    return true
                }
                .toolbar {
                    ToolbarItem(placement: .navigation) {
                        Button(action: { isShowingPalette.toggle() }) {
                            Label("Command Palette", systemImage: "magnifyingglass")
                        }
                        .keyboardShortcut("k", modifiers: .command)
                        
                        Toggle(isOn: $isBlockMode) {
                            Label("Block Mode", systemImage: "square.text.square")
                        }
                        .toggleStyle(.button)
                    }
                    ToolbarItem(placement: .status) {
                        HStack {
                            Text(doctorStatus)
                                .font(.caption)
                                .foregroundColor(SymairaTheme.textMuted)
                            if let lastEv = watcher.latestEvent {
                                Text("Last event: \(lastEv.event) on \(lastEv.path)")
                                    .font(.caption)
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
                            self.selectedNote = note
                        },
                        onSelectSearchResult: { result in
                            // For search results, we match the path to a Note
                            if let found = notes.first(where: { $0.path == result.path }) {
                                self.selectedNote = found
                            }
                        }
                    )
                }
                // App-wide shortcut for Cmd-K
                .onAppear {
                    NSEvent.addLocalMonitorForEvents(matching: .keyDown) { event in
                        if event.modifierFlags.contains(.command) && event.charactersIgnoringModifiers == "k" {
                            isShowingPalette.toggle()
                            return nil
                        }
                        return event
                    }
                }
                .onReceive(NotificationCenter.default.publisher(for: .openDiscover)) { _ in
                    displayMode = .discover
                }
                .overlay(alignment: .top) {
                    if notificationManager.isDenied {
                        NotificationDeniedBanner()
                    }
                }
                .onChange(of: notificationManager.deepLinkedDocumentPath) { path in
                    guard let path else { return }
                    deepLinkDocPath = path
                    displayMode = .docs
                    notificationManager.deepLinkedDocumentPath = nil
                }
                .task {
                    await fetchNotes()
                    await fetchViews()
                    await fetchDoctor()
                    await fetchDocCounts()
                }
                .onChange(of: watcher.latestEvent) { ev in
                    Task {
                        // Refresh notes if a file changed
                        await fetchNotes()
                        await fetchDocCounts()
                        // If the current note was changed externally, reload it
                        if let selected = selectedNote, ev?.path == selected.path {
                            await loadContent(for: selected)
                        }
                        // Refresh notifications when documents change
                        await notificationManager.refreshNotifications(with: core)
                    }
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
            self.notes = try await core.listFiles()
        } catch {
            print("Failed to list files: \(error)")
        }
    }

    private func fetchViews() async {
        do {
            self.dbViews = try await core.viewsList()
        } catch {
            print("Failed to list views: \(error)")
        }
    }

    private func fetchDoctor() async {
        do {
            self.doctorStatus = try await core.getDoctor()
        } catch {
            self.doctorStatus = "Doctor Error"
        }
    }

    private func loadContent(for note: Note) async {
        if let data = FileManager.default.contents(atPath: note.path),
           let string = String(data: data, encoding: .utf8) {
            self.noteContent = string
        } else {
            self.noteContent = "Error reading file."
        }
    }

    private func loadBacklinks(for note: Note) async {
        do {
            self.backlinks = try await core.backlinks(for: note.path)
        } catch {
            self.backlinks = []
        }
    }

    private func debouncedSave(note: Note, content: String) {
        saveTask?.cancel()
        saveTask = Task {
            try? await Task.sleep(nanoseconds: 500_000_000) // 500ms
            guard !Task.isCancelled else { return }

            // Atomic write
            if let data = content.data(using: .utf8) {
                let url = URL(fileURLWithPath: note.path)
                do {
                    try data.write(to: url, options: .atomic)
                    // Core events watcher will pick this up
                } catch {
                    print("Failed to save: \(error)")
                }
            }
        }
    }

    private func navigateToNote(title: String) {
        if let found = notes.first(where: { $0.title == title }) {
            self.selectedNote = found
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
            print("fetchDocCounts failed: \(error)")
        }
    }

    private func isConflicted(_ note: Note) -> Bool {
        return note.path.contains(" 2.md") || note.path.contains("conflicted copy")
    }
}

// MARK: - Notification Permission Denied Banner

private struct NotificationDeniedBanner: View {
    var body: some View {
        HStack {
            Image(systemName: "bell.slash")
                .foregroundColor(.white)
            Text("Notifications are disabled. Enable in System Settings > Notifications > SymDesk.")
                .font(.caption.bold())
                .foregroundColor(.white)
            Spacer()
            Button("Open Settings") {
                if let url = URL(string: "x-apple.systempreferences:com.apple.preference.notifications?PrivacyNotificationCenter") {
                    NSWorkspace.shared.open(url)
                }
            }
            .font(.caption)
            .foregroundColor(.white.opacity(0.9))
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(Color.orange)
    }
}
