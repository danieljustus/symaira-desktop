import SwiftUI
import AppKit
import Observation

struct SearchView: View {
    @Bindable var engineManager: EngineManager
    @State private var model = SearchModel()

    // JSON parsing helper structs
    struct SearchResult: Codable, Identifiable, Hashable {
        var id: String {
            return "\(chunk.document_path)-\(chunk.chunk_index)"
        }
        let chunk: Chunk
        let bm25_rank: Int
        let vector_rank: Int
        let rrf_score: Float
        let cosine_score: Float
        
        static func == (lhs: SearchResult, rhs: SearchResult) -> Bool {
            return lhs.id == rhs.id
        }
        
        func hash(into hasher: inout Hasher) {
            hasher.combine(id)
        }
    }

    struct Chunk: Codable {
        let id: Int64
        let uuid: String
        let document_path: String
        let chunk_index: Int
        let content: String
        let hash: String
    }

    var body: some View {
        @Bindable var model = model
        NavigationSplitView {
            // Left sidebar: Search controls & list of matches
            VStack(spacing: 12) {
                // Search Input Box
                HStack {
                    Image(systemName: "magnifyingglass")
                        .foregroundStyle(model.query.isEmpty ? Color.symairaMuted : Color.symairaGold)
                    
                    TextField("Search files and contents…", text: $model.query, onCommit: {
                        performSearch()
                    })
                    .textFieldStyle(.plain)
                    .foregroundStyle(Color.symairaText)
                    
                    if !model.query.isEmpty {
                        Button {
                            model.query = ""
                            model.results = []
                            model.selectedResult = nil
                        } label: {
                            Image(systemName: "xmark.circle.fill")
                                .foregroundStyle(Color.symairaMuted)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Clear search query")
                    }
                }
                .padding(10)
                .background(Color.symairaBgDarker)
                .cornerRadius(8)
                .overlay(
                    RoundedRectangle(cornerRadius: 8)
                        .stroke(model.query.isEmpty ? Color.symairaBorder : Color.symairaGold.opacity(0.4), lineWidth: 1)
                )
                .padding(.horizontal)
                .padding(.top, 16)
                
                // Server Warning
                if !engineManager.isRunning {
                    VStack(spacing: 8) {
                        Text("Search Daemon Stopped")
                            .font(.headline)
                            .foregroundStyle(Color.symairaText)
                        Text("Please start the search server from the Dashboard to query the local index.")
                            .font(.caption)
                            .multilineTextAlignment(.center)
                            .foregroundStyle(Color.symairaTextSecondary)
                    }
                    .padding()
                    .frame(maxWidth: .infinity)
                    .background(Color.red.opacity(0.1))
                    .cornerRadius(8)
                    .padding(.horizontal)
                    Spacer()
                } else {
                    // Search results count
                    HStack {
                        if model.isSearching {
                            ProgressView()
                                .controlSize(.small)
                            Text("Searching…")
                                .font(.caption)
                                .foregroundStyle(Color.symairaTextSecondary)
                        } else if !model.results.isEmpty {
                            Text("Found \(model.results.count) matches")
                                .font(.system(.caption, design: .monospaced))
                                .foregroundStyle(Color.symairaGold)
                        } else if shouldShowEmptyMessage(query: model.query, results: model.results, searchError: model.searchError) {
                            Text("No documents matched your query. Check the query or add documents to the index.")
                                .font(.caption)
                                .foregroundStyle(Color.symairaMuted)
                        }
                        Spacer()
                        
                        // Limit Picker
                        Picker("Limit", selection: $model.limit) {
                            Text("5").tag(5)
                            Text("10").tag(10)
                            Text("20").tag(20)
                            Text("50").tag(50)
                        }
                        .pickerStyle(.menu)
                        .frame(width: 80)
                        .labelsHidden()
                        .onChange(of: model.limit) { _, _ in
                            if !model.query.isEmpty { performSearch() }
                        }
                    }
                    .padding(.horizontal)
                    
                    if let error = model.searchError {
                        Text(error)
                            .font(.caption)
                            .foregroundStyle(.red)
                            .padding(.horizontal)
                    }
                    
                    // Matches List
                    List(model.results, selection: $model.selectedResult) { result in
                        SearchResultRow(result: result, isSelected: model.selectedResult?.id == result.id)
                            .listRowInsets(EdgeInsets())
                            .listRowBackground(Color.clear)
                            .onTapGesture {
                                model.selectedResult = result
                            }
                    }
                    .scrollContentBackground(.hidden)
                    .background(Color.clear)
                }
            }
            .frame(minWidth: 320)
            .background(Color.symairaBgDarker)
        } detail: {
            // Right detail pane: selected document details & preview
            if let result = model.selectedResult {
                ResultDetailView(result: result)
            } else {
                VStack(spacing: 12) {
                    Image(systemName: "doc.text.magnifyingglass")
                        .font(.system(size: 48))
                        .foregroundStyle(Color.symairaMuted)
                    
                    Text("Select a search match to view details")
                        .font(.headline)
                        .foregroundStyle(Color.symairaTextSecondary)
                    
                    Text("Use hybrid keyword and semantic ranking to pinpoint snippets in your codebase.")
                        .font(.caption)
                        .foregroundStyle(Color.symairaMuted)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Color.symairaBg)
            }
        }
    }

    private func performSearch() {
        let port = engineManager.port ?? 8080
        model.performSearch(port: port, apiToken: engineManager.apiToken)
    }
}

@MainActor
@Observable
class SearchModel {
    var query = ""
    var limit = 10
    var results: [SearchView.SearchResult] = []
    var selectedResult: SearchView.SearchResult?
    var isSearching = false
    var searchError: String?
    
    func performSearch(port: Int, apiToken: String?) {
        let cleanQuery = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleanQuery.isEmpty else {
            results = []
            selectedResult = nil
            return
        }
        
        isSearching = true
        searchError = nil
        
        var components = URLComponents(string: "http://127.0.0.1:\(port)/search")!
        components.queryItems = [
            URLQueryItem(name: "q", value: cleanQuery),
            URLQueryItem(name: "limit", value: "\(limit)")
        ]
        
        guard let url = components.url else {
            isSearching = false
            return
        }
        
        var request = URLRequest(url: url)
        if let apiToken {
            request.setValue("Bearer \(apiToken)", forHTTPHeaderField: "Authorization")
        }

        URLSession.shared.dataTask(with: request) { [weak self] data, response, error in
            Task { @MainActor [weak self] in
                guard let self else { return }
                self.isSearching = false
                if let error = error {
                    self.searchError = error.localizedDescription
                    return
                }
                
                guard let data = data else {
                    self.searchError = "No data returned from search server"
                    return
                }
                
                do {
                    let decoded = try JSONDecoder().decode([SearchView.SearchResult].self, from: data)
                    self.results = decoded
                    if let first = decoded.first {
                        self.selectedResult = first
                    } else {
                        self.selectedResult = nil
                    }
                } catch {
                    self.searchError = "Failed to parse results: \(error.localizedDescription)"
                    print("Parse error: \(error)")
                }
            }
        }.resume()
    }
}

/// True when the search UI should show the "no documents matched" empty
/// state: a query is present, no results came back, and the search did
/// not fail. An errored search must never also claim a zero-result
/// outcome (issue #309).
func shouldShowEmptyMessage(query: String, results: [SearchView.SearchResult], searchError: String?) -> Bool {
    return !query.isEmpty && results.isEmpty && searchError == nil
}

struct SearchResultRow: View {
    let result: SearchView.SearchResult
    let isSelected: Bool
    
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Image(systemName: "doc.text")
                    .foregroundStyle(isSelected ? Color.symairaBg : Color.symairaGold)
                
                Text(URL(fileURLWithPath: result.chunk.document_path).lastPathComponent)
                    .font(.headline)
                    .foregroundStyle(isSelected ? Color.symairaBg : Color.symairaText)
                
                Spacer()
                
                // RRF Badge
                Text(String(format: "RRF %.3f", result.rrf_score))
                    .font(.system(.caption2, design: .monospaced))
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(isSelected ? Color.symairaBg.opacity(0.2) : Color.symairaGold.opacity(0.12))
                    .foregroundStyle(isSelected ? Color.symairaBg : Color.symairaGold)
                    .cornerRadius(4)
            }
            
            Text(result.chunk.content.trimmingCharacters(in: .whitespacesAndNewlines))
                .lineLimit(2)
                .font(.caption)
                .foregroundStyle(isSelected ? Color.symairaBg.opacity(0.8) : Color.symairaTextSecondary)
            
            Text(result.chunk.document_path)
                .lineLimit(1)
                .truncationMode(.head)
                .font(.system(.caption2, design: .monospaced))
                .foregroundStyle(isSelected ? Color.symairaBg.opacity(0.6) : Color.symairaMuted)
        }
        .padding(12)
        .background(isSelected ? Color.symairaGold : Color.clear)
        .cornerRadius(8)
        .padding(.horizontal, 12)
        .padding(.vertical, 4)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Search result: \(URL(fileURLWithPath: result.chunk.document_path).lastPathComponent)")
        .accessibilityAddTraits(.isButton)
        .accessibilityValue(isSelected ? "Selected" : "")
    }
}

struct ResultDetailView: View {
    let result: SearchView.SearchResult
    
    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Header: Path and Open Button
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text("MATCH DETAILS")
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(Color.symairaGold)
                        .tracking(1)
                    
                    Text(URL(fileURLWithPath: result.chunk.document_path).lastPathComponent)
                        .font(.title3.bold())
                        .foregroundStyle(Color.symairaText)
                }
                Spacer()
                
                Button {
                    NSWorkspace.shared.open(URL(fileURLWithPath: result.chunk.document_path))
                } label: {
                    Label("Open File", systemImage: "arrow.up.forward.app")
                        .foregroundStyle(Color.symairaBg)
                }
                .buttonStyle(.borderedProminent)
                .tint(Color.symairaGold)
                .accessibilityLabel("Open file in default application")
            }
            .padding()
            .background(Color.symairaBgDarker)
            
            Divider()
                .background(Color.symairaBorder)
            
            // Detail ScrollView
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    
                    // Score grid
                    VStack(alignment: .leading, spacing: 10) {
                        Text("RANKING METRICS")
                            .font(.system(.caption, design: .monospaced))
                            .foregroundStyle(Color.symairaGold)
                            .bold()
                        
                        LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible())], spacing: 12) {
                            MetricCard(title: "RRF Fusion Score", value: String(format: "%.4f", result.rrf_score), subtitle: "Fused search ranking")
                            MetricCard(title: "Cosine Similarity", value: String(format: "%.4f", result.cosine_score), subtitle: "Semantic vector distance")
                            MetricCard(title: "BM25 Rank", value: result.bm25_rank > 0 ? "#\(result.bm25_rank)" : "—", subtitle: "Keyword search position")
                            MetricCard(title: "Vector Rank", value: result.vector_rank > 0 ? "#\(result.vector_rank)" : "—", subtitle: "Semantic search position")
                        }
                    }
                    
                    // File details
                    VStack(alignment: .leading, spacing: 8) {
                        Text("FILE INFO")
                            .font(.system(.caption, design: .monospaced))
                            .foregroundStyle(Color.symairaGold)
                            .bold()
                        
                        VStack(alignment: .leading, spacing: 6) {
                            LabeledInfoRow(label: "Full Path", value: result.chunk.document_path)
                            LabeledInfoRow(label: "Snippet UUID", value: result.chunk.uuid)
                            LabeledInfoRow(label: "Chunk Index", value: "\(result.chunk.chunk_index)")
                        }
                        .padding()
                        .background(Color.symairaCard)
                        .cornerRadius(8)
                        .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color.symairaBorder, lineWidth: 1))
                    }
                    
                    // Text Snippet Content
                    VStack(alignment: .leading, spacing: 10) {
                        Text("MATCHING SNIPPET CONTENT")
                            .font(.system(.caption, design: .monospaced))
                            .foregroundStyle(Color.symairaGold)
                            .bold()
                        
                        Text(result.chunk.content)
                            .font(.system(.body, design: .monospaced))
                            .foregroundStyle(Color.symairaText)
                            .padding()
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(Color.symairaBgDarker)
                            .cornerRadius(8)
                            .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color.symairaBorder, lineWidth: 1))
                    }
                }
                .padding()
            }
        }
        .background(Color.symairaBg)
    }
}

struct MetricCard: View {
    let title: String
    let value: String
    let subtitle: String
    
    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title)
                .font(.caption2)
                .foregroundStyle(Color.symairaTextSecondary)
            
            Text(value)
                .font(.title2.bold())
                .foregroundStyle(Color.symairaGold)
            
            Text(subtitle)
                .font(.system(.caption2, design: .monospaced))
                .foregroundStyle(Color.symairaMuted)
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.symairaCard)
        .cornerRadius(8)
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color.symairaBorder, lineWidth: 1))
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(title): \(value), \(subtitle)")
    }
}

struct LabeledInfoRow: View {
    let label: String
    let value: String
    
    var body: some View {
        HStack(alignment: .top) {
            Text(label)
                .font(.caption)
                .foregroundStyle(Color.symairaGold)
                .frame(width: 90, alignment: .leading)
            
            Text(value)
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(Color.symairaText)
                .textSelection(.enabled)
            
            Spacer()
        }
    }
}
