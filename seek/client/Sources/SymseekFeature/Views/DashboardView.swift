import SwiftUI
import Observation

struct DashboardView: View {
    @Bindable var engineManager: EngineManager
    @State private var model = DashboardModel()
    @State private var timer: Timer?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                // Header Area
                HStack {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("DASHBOARD")
                            .font(.system(.caption, design: .monospaced))
                            .foregroundStyle(Color.symairaGold)
                            .tracking(2)
                        
                        Text("Search Index Overview")
                            .font(.title2.bold())
                            .foregroundStyle(Color.symairaText)
                    }
                    Spacer()
                    
                    // Daemon Status Badge
                    HStack(spacing: 8) {
                        Circle()
                            .fill(statusColor)
                            .frame(width: 8, height: 8)
                            .shadow(color: statusColor, radius: 4)
                        
                        Text(statusText)
                            .font(.system(.body, design: .monospaced))
                            .foregroundStyle(Color.symairaText)
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 6)
                    .background(Color.symairaCard)
                    .cornerRadius(8)
                    .overlay(
                        RoundedRectangle(cornerRadius: 8)
                            .stroke(Color.symairaBorder, lineWidth: 1)
                    )
                }
                
                // Connection Info Card
                VStack(alignment: .leading, spacing: 12) {
                    HStack {
                        Image(systemName: "server.rack")
                            .font(.title3)
                            .foregroundStyle(Color.symairaGold)
                        Text("Local Search Daemon")
                            .font(.headline)
                            .foregroundStyle(Color.symairaText)
                        Spacer()
                    }
                    
                    Text("The local-first hybrid engine indexes your codebase and documents using SQLite FTS5 for keyword matching and cosine vector similarity for semantic search.")
                        .font(.body)
                        .foregroundStyle(Color.symairaTextSecondary)
                        .fixedSize(horizontal: false, vertical: true)
                    
                    Divider()
                        .background(Color.symairaBorder)
                        .padding(.vertical, 4)
                    
                    HStack {
                        if engineManager.isRunning {
                            Text("REST Port: \(String(engineManager.port ?? 8080))")
                                .font(.system(.caption, design: .monospaced))
                                .foregroundStyle(Color.symairaTextSecondary)
                            Spacer()
                            Button("Stop Server") {
                                // Issue #306: never run the supervisor's stop
                                // path synchronously on the main actor — it can
                                // block on an internal lock and freeze the app.
                                // The action returns immediately; the stop runs
                                // on EngineManager's background stop queue.
                                Task {
                                    await engineManager.stop()
                                }
                            }
                            .buttonStyle(.borderedProminent)
                            .tint(.red.opacity(0.8))
                            .accessibilityLabel("Stop the search server")
                        } else {
                            Text("Search server is currently inactive.")
                                .font(.system(.caption, design: .monospaced))
                                .foregroundStyle(Color.symairaMuted)
                            Spacer()
                            Button("Start Server") {
                                Task {
                                    await engineManager.start()
                                }
                            }
                            .buttonStyle(.borderedProminent)
                            .tint(Color.symairaGold)
                            .foregroundStyle(Color.symairaBg)
                            .accessibilityLabel("Start the search server")
                        }
                    }
                }
                .symairaCard()
                .accessibilityElement(children: .contain)
                
                // Stat Grid
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 180))], spacing: 16) {
                    StatCard(
                        title: "INDEXED DOCUMENTS",
                        value: "\(model.documentCount)",
                        icon: "doc.text.fill",
                        description: "Source files indexed"
                    )
                    
                    StatCard(
                        title: "VECTOR CHUNKS",
                        value: "\(model.chunkCount)",
                        icon: "rectangle.stack.fill",
                        description: "Document snippets"
                    )
                    
                    StatCard(
                        title: "DATABASE SIZE",
                        value: model.databaseSize,
                        icon: "cylinder.split.1x2.fill",
                        description: "SQLite storage on disk"
                    )
                }
                
                // Quick Start / Ecosystem
                VStack(alignment: .leading, spacing: 12) {
                    Text("SYMAIRA SEEK ECOSYSTEM")
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(Color.symairaGold)
                        .tracking(1)
                    
                    Text("This node is running the core self-hosted search engine. It integrates seamlessly with Claude, Cursor, and ChatGPT via the Model Context Protocol (MCP) or standard REST requests.")
                        .font(.callout)
                        .foregroundStyle(Color.symairaTextSecondary)
                }
                .symairaCard()
                .accessibilityElement(children: .contain)
            }
            .padding()
        }
        .background(Color.symairaBg)
        .onAppear {
            startPolling()
        }
        .onDisappear {
            stopPolling()
        }
        .onChange(of: engineManager.isRunning) { _, isRunning in
            if isRunning {
                fetchStats()
            } else {
                model.resetStats()
            }
        }
    }
    
    private var statusColor: Color {
        switch engineManager.state {
        case .stopped: return .gray
        case .starting: return .orange
        case .running: return Color.symairaGold
        case .failed: return .red
        }
    }
    
    private var statusText: String {
        switch engineManager.state {
        case .stopped: return "STOPPED"
        case .starting: return "STARTING…"
        case .running: return "RUNNING"
        case .failed: return "FAILED"
        }
    }
    
    private func startPolling() {
        fetchStats()
        timer = Timer.scheduledTimer(withTimeInterval: 3.0, repeats: true) { _ in
            Task { @MainActor in
                self.fetchStats()
            }
        }
    }
    
    private func stopPolling() {
        timer?.invalidate()
        timer = nil
    }
    
    private func fetchStats() {
        let port = engineManager.port ?? 8080
        model.fetchStats(port: port, isDaemonRunning: engineManager.isRunning, apiToken: engineManager.apiToken)
    }
}

struct StatCard: View {
    let title: String
    let value: String
    let icon: String
    let description: String
    
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Image(systemName: icon)
                    .foregroundStyle(Color.symairaGold)
                    .font(.title3)
                Spacer()
            }
            
            Text(value)
                .font(.system(.largeTitle, design: .rounded))
                .bold()
                .foregroundStyle(Color.symairaText)
            
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(Color.symairaGold)
                    .bold()
                Text(description)
                    .font(.caption)
                    .foregroundStyle(Color.symairaTextSecondary)
            }
        }
        .symairaCard()
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(title): \(value), \(description)")
    }
}

@MainActor
@Observable
class DashboardModel {
    var documentCount = 0
    var chunkCount = 0
    var databaseSize = "0 B"
    var isFetching = false
    
    func fetchStats(port: Int, isDaemonRunning: Bool, apiToken: String?) {
        guard isDaemonRunning, !isFetching else { return }
        isFetching = true
        
        guard let url = URL(string: "http://127.0.0.1:\(port)/status") else {
            isFetching = false
            return
        }
        
        var request = URLRequest(url: url)
        request.timeoutInterval = 2.0
        if let apiToken {
            request.setValue("Bearer \(apiToken)", forHTTPHeaderField: "Authorization")
        }
        
        URLSession.shared.dataTask(with: request) { [weak self] data, response, error in
            guard let self else { return }
            
            struct BackendStats: Codable {
                let document_count: Int
                let chunk_count: Int
                let database_size_bytes: Int64
            }
            
            guard let data = data, error == nil else {
                Task { @MainActor in
                    self.isFetching = false
                }
                return
            }
            
            do {
                let stats = try JSONDecoder().decode(BackendStats.self, from: data)
                
                let formatter = ByteCountFormatter()
                formatter.countStyle = .file
                let sizeStr = formatter.string(fromByteCount: stats.database_size_bytes)
                
                Task { @MainActor in
                    self.documentCount = stats.document_count
                    self.chunkCount = stats.chunk_count
                    self.databaseSize = sizeStr
                    self.isFetching = false
                }
            } catch {
                print("Failed to parse status: \(error)")
                Task { @MainActor in
                    self.isFetching = false
                }
            }
        }.resume()
    }
    
    func resetStats() {
        documentCount = 0
        chunkCount = 0
        databaseSize = "0 B"
    }
}
