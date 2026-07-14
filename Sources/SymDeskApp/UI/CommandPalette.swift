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
    @State private var searchHint: String?
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
                TextField("Search notes or create a new one…", text: $searchText)
                    .textFieldStyle(.plain)
                    .font(.title2)
                    .foregroundColor(SymairaTheme.textPrimary)
                    .onSubmit {
                        performSearch()
                    }
                if !searchText.isEmpty {
                    Button {
                        searchText = ""
                        searchResults = []
                        searchHint = nil
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(SymairaTheme.textMuted)
                    }
                    .buttonStyle(.plain)
                    .help("Clear search")
                }
                Text("↩")
                    .font(.caption.monospaced())
                    .foregroundStyle(SymairaTheme.textMuted)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
            .padding(16)

            Text("Operators: path:, tag:, type:, status:, \"exact phrase\", -exclude, /regex/")
                .font(.caption)
                .foregroundColor(SymairaTheme.textMuted)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal)
                .padding(.bottom, 8)

            if isSearching {
                ProgressView()
                    .tint(SymairaTheme.goldPrimary)
                    .padding()
            }
            
            if let err = errorMessage {
                Text(err).foregroundColor(.red).padding()
            }

            if let hint = searchHint {
                Label(hint, systemImage: "info.circle")
                    .font(.caption)
                    .foregroundColor(SymairaTheme.goldSecondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal)
                    .padding(.bottom, 8)
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
                        
                        Button("Search: '\(searchText)'") {
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
            .listStyle(.inset)
            .scrollContentBackground(.hidden)
        }
        .background {
            SymairaScreen { Color.clear }
        }
        .frame(width: 640, height: 460)
        .onDisappear {
            searchText = ""
            searchResults = []
            searchHint = nil
            errorMessage = nil
        }
    }
    
    private func performSearch() {
        guard !searchText.isEmpty else { return }
        isSearching = true
        errorMessage = nil
        searchHint = nil
        
        Task {
            do {
                let response = try await core.search(query: searchText)
                await MainActor.run {
                    self.searchResults = response.results
                    self.searchHint = response.hint
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
