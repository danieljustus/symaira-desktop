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
    @State private var activeFilters: [SearchFilter] = []
    /// Set when retrieval is degraded (cold index or unreachable embedding
    /// backend). Search still answers in that state, so without this the user
    /// only sees thin results and no reason (issue #515).
    @State private var retrievalWarning: String?
    
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
                    .symairaText(.callout)
                    .foregroundColor(SymairaTheme.goldPrimary)
                TextField("Search notes or create a new one…", text: $searchText)
                    .textFieldStyle(.plain)
                    .symairaText(.title)
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
                    .symairaText(.caption).monospaced()
                    .foregroundStyle(SymairaTheme.textMuted)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
            .padding(16)

            FilterChipsView(filters: $activeFilters)
                .padding(.horizontal)
                .padding(.bottom, 4)

            Text("Filters compile to query syntax. Type raw queries or use the Filter button above.")
                .symairaText(.caption)
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

            if let retrievalWarning {
                Label(retrievalWarning, systemImage: "exclamationmark.triangle")
                    .symairaText(.caption)
                    .foregroundColor(.orange)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal)
                    .padding(.bottom, 8)
            }

            if let hint = searchHint {
                Label(hint, systemImage: "info.circle")
                    .symairaText(.caption)
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
                                        .symairaText(.subheading)
                                        .foregroundColor(SymairaTheme.textPrimary)
                                    Text(res.snippet)
                                        .symairaText(.caption)
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
        .onExitCommand {
            if !searchText.isEmpty {
                searchText = ""
                searchResults = []
                searchHint = nil
            } else if !activeFilters.isEmpty {
                activeFilters = []
            } else {
                isPresented = false
            }
        }
        .onDisappear {
            searchText = ""
            searchResults = []
            searchHint = nil
            errorMessage = nil
            activeFilters = []
            retrievalWarning = nil
        }
    }
    
    private func performSearch() {
        guard !searchText.isEmpty || !activeFilters.isEmpty else { return }
        isSearching = true
        errorMessage = nil
        searchHint = nil

        let filterQuery = activeFilters.map(\.queryString).joined(separator: " ")
        let fullQuery = [filterQuery, searchText]
            .filter { !$0.isEmpty }
            .joined(separator: " ")
        
        Task {
            await checkRetrievalHealth()
            do {
                let response = try await core.search(query: fullQuery)
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
    
    /// Reads the retrieval state once per palette session. A failure here is
    /// not surfaced: it must never turn a working search into an error.
    private func checkRetrievalHealth() async {
        guard retrievalWarning == nil, let status = try? await core.retrievalStatus() else { return }
        if status.isEmpty {
            retrievalWarning = "The search index is empty — results come from plain full-text matching. Open Search Index to build it."
        } else if !status.backendAvailable {
            retrievalWarning = "The embedding backend is unreachable — semantic ranking is degraded. See Search Index."
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
                _ = try await core.noteDaily()
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
