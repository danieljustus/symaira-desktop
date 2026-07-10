import SwiftUI
import SymairaTheme
import SymDeskCore
import SymairaCLIRunner

struct CommandPalette: View {
    @EnvironmentObject var core: DeskCore
    @Binding var isPresented: Bool
    @Binding var allNotes: [Note]
    
    var onSelectNote: (Note) -> Void
    var onSelectSearchResult: (SearchResult) -> Void
    
    @State private var searchText = ""
    @State private var searchResults: [SearchResult] = []
    @State private var isSearching = false
    @State private var errorMessage: String?
    
    var filteredNotes: [Note] {
        if searchText.isEmpty {
            return allNotes
        }
        return allNotes.filter { $0.title.localizedCaseInsensitiveContains(searchText) }
    }
    
    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Image(systemName: "magnifyingglass")
                    .font(.system(size: 18))
                    .foregroundColor(SymairaTheme.goldPrimary)
                TextField("Search notes or type to create...", text: $searchText)
                    .textFieldStyle(.plain)
                    .font(.system(size: 24))
                    .foregroundColor(SymairaTheme.textPrimary)
                    .onSubmit {
                        performSearch()
                    }
            }
            .padding(12)
            .background(SymairaTheme.bgCard)
            .overlay(
                RoundedRectangle(cornerRadius: 10)
                    .stroke(SymairaTheme.borderGlassHover, lineWidth: 1)
            )
            .cornerRadius(10)
            .padding()

            if isSearching {
                ProgressView()
                    .tint(SymairaTheme.goldPrimary)
                    .padding()
            }
            
            if let err = errorMessage {
                Text(err).foregroundColor(.red).padding()
            }
            
            List {
                if !searchText.isEmpty {
                    Section("Actions") {
                        Button("Create Note: '\(searchText)'") {
                            createNote()
                        }
                        .buttonStyle(PlainButtonStyle())
                        
                        Button("Create Daily Note") {
                            createDailyNote()
                        }
                        .buttonStyle(PlainButtonStyle())
                        
                        Button("Full-Text Search: '\(searchText)'") {
                            performSearch()
                        }
                        .buttonStyle(PlainButtonStyle())
                    }
                }
                
                if !searchResults.isEmpty {
                    Section("Search Results") {
                        ForEach(searchResults) { res in
                            Button(action: {
                                isPresented = false
                                onSelectSearchResult(res)
                            }) {
                                VStack(alignment: .leading) {
                                    Text(res.title)
                                        .font(.headline)
                                        .foregroundColor(SymairaTheme.textPrimary)
                                    Text(res.snippet)
                                        .font(.caption)
                                        .foregroundColor(SymairaTheme.textSecondary)
                                }
                            }
                            .buttonStyle(PlainButtonStyle())
                        }
                    }
                }
                
                Section("Files") {
                    ForEach(filteredNotes) { note in
                        Button(action: {
                            isPresented = false
                            onSelectNote(note)
                        }) {
                            Text(note.title)
                        }
                        .buttonStyle(PlainButtonStyle())
                    }
                }
            }
        }
        .scrollContentBackground(.hidden)
        .background(SymairaTheme.bgDark)
        .frame(width: 600, height: 400)
        .onDisappear {
            searchText = ""
            searchResults = []
            errorMessage = nil
        }
    }
    
    private func performSearch() {
        guard !searchText.isEmpty else { return }
        isSearching = true
        errorMessage = nil
        
        Task {
            do {
                let results = try await core.search(query: searchText)
                await MainActor.run {
                    self.searchResults = results
                    self.isSearching = false
                }
            } catch {
                await MainActor.run {
                    self.errorMessage = "Search failed: \(error.localizedDescription)"
                    self.isSearching = false
                }
            }
        }
    }
    
    private func createNote() {
        guard !searchText.isEmpty else { return }
        isSearching = true
        Task {
            do {
                let _ = try await core.noteNew(title: searchText)
                // The EventWatcher will catch the new file and update `allNotes`.
                // We just close the palette.
                await MainActor.run {
                    self.isPresented = false
                    self.isSearching = false
                }
            } catch {
                await MainActor.run {
                    self.errorMessage = "Create failed: \(error.localizedDescription)"
                    self.isSearching = false
                }
            }
        }
    }
    private func createDailyNote() {
        isSearching = true
        Task {
            do {
                guard let tool = core.tool else { throw DeskCoreError.coreNotFound }
                let runner = CLIRunner()
                // Use runDecoding to execute symdesk note daily --json
                struct DailyResult: Codable { let path: String }
                var args = ["note", "daily", "--json"]
                if let vp = core.vaultPath, !vp.isEmpty {
                    args.append(contentsOf: ["--vault", vp])
                }
                
                _ = try await runner.runDecoding(
                    DailyResult.self,
                    executable: tool.location.url,
                    arguments: args
                )
                
                await MainActor.run {
                    self.isPresented = false
                    self.isSearching = false
                }
            } catch {
                await MainActor.run {
                    self.errorMessage = "Daily note failed: \(error.localizedDescription)"
                    self.isSearching = false
                }
            }
        }
    }
}
