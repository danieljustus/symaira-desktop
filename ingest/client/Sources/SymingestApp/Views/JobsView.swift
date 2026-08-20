import SwiftUI
import AppKit

struct JobsView: View {
    @Environment(ConfigStore.self) private var configStore
    
    @State private var jobs: [IngestJob] = []
    @State private var searchText: String = ""
    @State private var selectedStatus: String = "All"
    @State private var isLoading = false
    @State private var errorMessage: String?
    @State private var timer: Timer?
    
    let statuses = ["All", "pending", "running", "completed", "failed", "skipped"]
    
    var filteredJobs: [IngestJob] {
        jobs.filter { job in
            let matchesSearch = searchText.isEmpty || job.sourcePath.localizedCaseInsensitiveContains(searchText)
            let matchesStatus = selectedStatus == "All" || job.status.lowercased() == selectedStatus.lowercased()
            return matchesSearch && matchesStatus
        }
    }
    
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            // Header
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Ingestion Jobs")
                        .font(Font.title.bold())
                        .foregroundStyle(Theme.textPrimary)
                    Text("Monitor document consumption jobs in the queue.")
                        .font(.subheadline)
                        .foregroundStyle(Theme.textSecondary)
                }
                Spacer()
                
                Button(action: { Task { await loadJobs() } }) {
                    Label("Refresh", systemImage: "arrow.clockwise")
                }
                .disabled(isLoading)
                .buttonStyle(.bordered)
            }
            
            // Filters
            HStack(spacing: 16) {
                // Search bar
                HStack {
                    Image(systemName: "magnifyingglass")
                        .foregroundStyle(Theme.textSecondary)
                    TextField("Search source path...", text: $searchText)
                        .textFieldStyle(.plain)
                        .foregroundStyle(Theme.textPrimary)
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                .background(Color.black.opacity(0.3))
                .clipShape(RoundedRectangle(cornerRadius: 6))
                .overlay(
                    RoundedRectangle(cornerRadius: 6)
                        .stroke(Theme.borderGlass, lineWidth: 1)
                )
                
                // Status Filter
                Picker("Status", selection: $selectedStatus) {
                    ForEach(statuses, id: \.self) { status in
                        Text(status.capitalized).tag(status)
                    }
                }
                .pickerStyle(.menu)
                .frame(width: 150)
            }
            
            // Error Message
            if let error = errorMessage {
                Text(error)
                    .foregroundStyle(.red)
                    .font(.caption)
                    .padding()
                    .background(Color.red.opacity(0.1))
                    .clipShape(RoundedRectangle(cornerRadius: 6))
            }
            
            // Table
            if isLoading && jobs.isEmpty {
                VStack {
                    Spacer()
                    ProgressView("Loading jobs...")
                    Spacer()
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if filteredJobs.isEmpty {
                VStack {
                    Spacer()
                    ContentUnavailableView(
                        "No jobs found",
                        systemImage: "list.bullet.rectangle.portrait",
                        description: Text("Try changing your search text or status filter.")
                    )
                    Spacer()
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                List {
                    // Custom Table Header
                    HStack {
                        Text("ID").frame(width: 40, alignment: .leading)
                        Text("Status").frame(width: 90, alignment: .leading)
                        Text("Attempts").frame(width: 70, alignment: .center)
                        Text("Kind").frame(width: 70, alignment: .leading)
                        Text("Source Path").frame(maxWidth: .infinity, alignment: .leading)
                        Text("Action").frame(width: 180, alignment: .trailing)
                    }
                    .font(.subheadline.bold())
                    .foregroundStyle(Theme.textSecondary)
                    .padding(.vertical, 8)
                    .listRowBackground(Color.clear)
                    
                    Divider()
                        .listRowBackground(Color.clear)
                    
                    ForEach(filteredJobs) { job in
                        HStack {
                            Text("\(job.id)")
                                .font(.caption.monospaced())
                                .foregroundStyle(Theme.textSecondary)
                                .frame(width: 40, alignment: .leading)
                            
                            // Status Badge
                            Text(job.status.uppercased())
                                .font(.system(size: 10, weight: .bold, design: .monospaced))
                                .padding(.horizontal, 6)
                                .padding(.vertical, 3)
                                .background(badgeColor(for: job.status).opacity(0.2))
                                .foregroundStyle(badgeColor(for: job.status))
                                .clipShape(RoundedRectangle(cornerRadius: 4))
                                .frame(width: 90, alignment: .leading)
                            
                            Text("\(job.attempts)")
                                .font(.body.monospaced())
                                .foregroundStyle(Theme.textPrimary)
                                .frame(width: 70, alignment: .center)
                            
                            Text(job.kind)
                                .font(.caption)
                                .foregroundStyle(Theme.textSecondary)
                                .frame(width: 70, alignment: .leading)
                            
                            VStack(alignment: .leading, spacing: 2) {
                                Text(job.sourcePath)
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                                    .foregroundStyle(Theme.textPrimary)
                                if let err = job.lastError {
                                    Text(err)
                                        .font(.caption2)
                                        .foregroundStyle(.red)
                                        .lineLimit(2)
                                }
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            
                            // Action Buttons
                            HStack(spacing: 6) {
                                Button("Reveal") {
                                    reveal(path: job.sourcePath)
                                }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                                Button("Error JSON") {
                                    reveal(path: job.sourcePath + ".error.json")
                                }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                                .disabled(!FileManager.default.fileExists(atPath: job.sourcePath + ".error.json"))
                                if job.status.lowercased() == "failed" {
                                    Button("Retry") {
                                        Task {
                                            await retryJob(id: job.id)
                                        }
                                    }
                                    .buttonStyle(.borderedProminent)
                                    .controlSize(.small)
                                }
                            }
                            .frame(width: 180, alignment: .trailing)
                        }
                        .padding(.vertical, 6)
                        .listRowBackground(Theme.bgCard)
                    }
                }
                .listStyle(.plain)
                .background(Color.clear)
            }
        }
        .onAppear {
            Task {
                await loadJobs()
            }
            startTimer()
        }
        .onDisappear {
            stopTimer()
        }
    }
    
    private func loadJobs() async {
        isLoading = true
        errorMessage = nil
        do {
            self.jobs = try await CLIClient.shared.listJobs(config: configStore)
        } catch {
            errorMessage = "Failed to load jobs: \(error.localizedDescription)"
        }
        isLoading = false
    }
    
    private func retryJob(id: Int64) async {
        let (success, message) = await CLIClient.shared.retryJob(id: id, config: configStore)
        if success {
            await loadJobs()
        } else {
            errorMessage = "Retry failed: \(message)"
        }
    }
    
    private func startTimer() {
        timer = Timer.scheduledTimer(withTimeInterval: 4.0, repeats: true) { _ in
            Task {
                await loadJobs()
            }
        }
    }
    
    private func stopTimer() {
        timer?.invalidate()
        timer = nil
    }

    private func reveal(path: String) {
        guard !path.isEmpty else { return }
        NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)])
    }
    
    private func badgeColor(for status: String) -> Color {
        switch status.lowercased() {
        case "completed", "done": return .green
        case "running": return Theme.goldPrimary
        case "failed": return .red
        case "skipped": return .orange
        default: return .gray
        }
    }
}
