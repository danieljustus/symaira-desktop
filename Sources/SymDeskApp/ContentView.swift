import SwiftUI
import SymairaTheme
import SymDeskCore

struct ContentView: View {
    @EnvironmentObject var core: DeskCore
    @EnvironmentObject var watcher: EventWatcher
    
    @State private var notes: [Note] = []
    @State private var selectedNote: Note?
    @State private var noteContent: String = ""
    @State private var doctorStatus: String = "Loading..."
    
    @State private var isShowingPalette = false
    @State private var isShowingPreview = false
    @State private var isShowingInspector = false
    @State private var backlinks: [String] = []
    
    // Auto-save debounce
    @State private var saveTask: Task<Void, Never>? = nil
    
    enum DisplayMode {
        case vault
        case graph
        case dbView
    }
    
    @State private var displayMode: DisplayMode = .vault
    @State private var selectedViewID: String?
    @State private var dbViews: [DbView] = []
    
    var body: some View {
        Group {
            if !core.isReady {
                if let err = core.errorMessage {
                    VStack {
                        Text("Error").font(.title).foregroundColor(.red)
                        Text(err)
                        Text("Run `brew install danieljustus/tap/symdesk` to install the core CLI.")
                            .padding(.top)
                    }
                    .padding()
                } else {
                    ProgressView("Connecting to SymDesk Core...")
                }
            } else {
                NavigationSplitView {
                    List {
                        Section("Views") {
                            Button("Vault") { displayMode = .vault }
                            Button("Graph") { displayMode = .graph }
                        }
                        
                        if !dbViews.isEmpty {
                            Section("Saved Views") {
                                ForEach(dbViews) { view in
                                    Button(view.name) {
                                        selectedViewID = view.id
                                        displayMode = .dbView
                                    }
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
                    .navigationTitle("SymDesk")
                } detail: {
                    switch displayMode {
                    case .graph:
                        GraphView { selectedNodeID in
                            navigateToNote(title: selectedNodeID)
                            displayMode = .vault
                        }
                    case .dbView:
                        if let vid = selectedViewID {
                            DbViewTable(viewID: vid)
                        } else {
                            Text("Select a view")
                        }
                    case .vault:
                        if let note = selectedNote {
                            VStack(spacing: 0) {
                                if isConflicted(note) {
                                    Text("⚠️ iCloud Conflict detected")
                                        .font(.caption)
                                        .frame(maxWidth: .infinity)
                                        .padding(4)
                                        .background(Color.yellow.opacity(0.3))
                                        .foregroundColor(.yellow)
                                }
                                
                                HStack(spacing: 0) {
                                    MarkdownEditorView(text: $noteContent, onLinkClick: { targetTitle in
                                        navigateToNote(title: targetTitle)
                                    })
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
                                    Button(action: { isShowingInspector.toggle() }) {
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
                                .foregroundColor(.secondary)
                        }
                    }
                }
                .inspector(isPresented: $isShowingInspector) {
                    VStack(alignment: .leading) {
                        Text("Backlinks").font(.headline).padding()
                        List(backlinks, id: \.self) { link in
                            Button(link) {
                                navigateToNote(title: link)
                            }
                            .buttonStyle(PlainButtonStyle())
                        }
                    }
                    .frame(minWidth: 200)
                }
                .toolbar {
                    ToolbarItem(placement: .status) {
                        HStack {
                            Text(doctorStatus)
                                .font(.caption)
                                .foregroundColor(.secondary)
                            if let lastEv = watcher.latestEvent {
                                Text("Last event: \(lastEv.event) on \(lastEv.path)")
                                    .font(.caption)
                                    .foregroundColor(.secondary)
                            }
                        }
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
                .task {
                    await fetchNotes()
                    await fetchViews()
                    await fetchDoctor()
                }
                .onChange(of: watcher.latestEvent) { ev in
                    Task {
                        // Refresh notes if a file changed
                        await fetchNotes()
                        // If the current note was changed externally, reload it
                        if let selected = selectedNote, ev?.path == selected.path {
                            await loadContent(for: selected)
                        }
                    }
                }
            }
        }
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
    
    private func isConflicted(_ note: Note) -> Bool {
        return note.path.contains(" 2.md") || note.path.contains("conflicted copy")
    }
}
