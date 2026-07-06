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
                    List(notes, selection: $selectedNote) { note in
                        Text(note.title)
                            .tag(note)
                    }
                    .navigationTitle("Vault")
                } detail: {
                    if let note = selectedNote {
                        ScrollView {
                            Text(noteContent)
                                .padding()
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .navigationTitle(note.title)
                        .task(id: note.id) {
                            await loadContent(for: note)
                        }
                    } else {
                        Text("Select a note")
                            .foregroundColor(.secondary)
                    }
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
                .task {
                    await fetchNotes()
                    await fetchDoctor()
                }
                .onChange(of: watcher.latestEvent) { _ in
                    Task { await fetchNotes() }
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
    
    private func fetchDoctor() async {
        do {
            self.doctorStatus = try await core.getDoctor()
        } catch {
            self.doctorStatus = "Doctor Error"
        }
    }
    
    private func loadContent(for note: Note) async {
        // Direct file read
        if let data = FileManager.default.contents(atPath: note.path),
           let string = String(data: data, encoding: .utf8) {
            self.noteContent = string
        } else {
            self.noteContent = "Error reading file."
        }
    }
}
