import SwiftUI
import UniformTypeIdentifiers

struct DashboardView: View {
    @Environment(ConfigStore.self) private var configStore
    @Environment(EngineManager.self) private var engineManager
    
    @State private var isTargeted = false
    @State private var isIngesting = false
    @State private var ingestResult: String?
    @State private var serviceResult: String?
    @State private var searchResult: String?
    @State private var isServiceBusy = false
    @State private var isSearchBusy = false
    @State private var isResultSuccess = true
    
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            // Header
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Inbox Dashboard")
                        .font(.title.bold())
                        .foregroundStyle(Theme.textPrimary)
                    Text("Supervise the inbox watcher daemon and ingest files manually.")
                        .font(.subheadline)
                        .foregroundStyle(Theme.textSecondary)
                }
                Spacer()
            }
            
            // Watcher Control Card
            HStack(spacing: 20) {
                // Status panel
                VStack(alignment: .leading, spacing: 12) {
                    HStack(spacing: 12) {
                        Circle()
                            .fill(statusColor)
                            .frame(width: 12, height: 12)
                            .shadow(color: statusColor.opacity(0.5), radius: 6)
                        
                        Text("Inbox Watcher Status:")
                            .font(.headline)
                            .foregroundStyle(Theme.textSecondary)
                        
                        Text(statusText)
                            .font(.headline.bold())
                            .foregroundStyle(Theme.textPrimary)
                    }
                    
                    if configStore.inboxPath.isEmpty {
                        Text("No inbox path configured. Set it in Settings to enable the background watcher.")
                            .font(.caption)
                            .foregroundStyle(.orange)
                    } else {
                        Text("Watching: \(configStore.inboxPath)")
                            .font(.caption.monospaced())
                            .foregroundStyle(Theme.textMuted)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                }
                
                Spacer()
                
                // Toggle Button
                if engineManager.isRunning {
                    Button(role: .destructive) {
                        engineManager.stop()
                    } label: {
                        Label("Stop Watcher", systemImage: "stop.fill")
                            .font(.headline)
                            .padding(.horizontal, 16)
                            .padding(.vertical, 8)
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                } else {
                    Button {
                        Task {
                            await engineManager.start(config: configStore)
                        }
                    } label: {
                        Label("Start Watcher", systemImage: "play.fill")
                            .font(.headline)
                            .padding(.horizontal, 16)
                            .padding(.vertical, 8)
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(Theme.goldPrimary)
                    .foregroundStyle(Theme.bgDark)
                    .controlSize(.large)
                    .disabled(configStore.inboxPath.isEmpty)
                }
            }
            .padding()
            .background(Theme.bgCard)
            .clipShape(RoundedRectangle(cornerRadius: 12))
            .overlay(
                RoundedRectangle(cornerRadius: 12)
                    .stroke(Theme.borderGlass, lineWidth: 1)
            )

            // LaunchAgent service controls
            VStack(alignment: .leading, spacing: 10) {
                HStack {
                    Label("LaunchAgent Service", systemImage: "gearshape.2.fill")
                        .font(.headline)
                        .foregroundStyle(Theme.textPrimary)
                    Spacer()
                    Button("Dry-run Install") { Task { await runService("install", dryRun: true) } }
                        .buttonStyle(.bordered)
                        .disabled(isServiceBusy || configStore.inboxPath.isEmpty)
                    Button("Install") { Task { await runService("install") } }
                        .buttonStyle(.bordered)
                        .disabled(isServiceBusy || configStore.inboxPath.isEmpty)
                    Button("Start") { Task { await runService("start") } }
                        .buttonStyle(.borderedProminent)
                        .tint(Theme.goldPrimary)
                        .disabled(isServiceBusy || configStore.inboxPath.isEmpty)
                    Button("Stop") { Task { await runService("stop") } }
                        .buttonStyle(.bordered)
                        .disabled(isServiceBusy || configStore.inboxPath.isEmpty)
                    Button("Status") { Task { await runService("status", json: false) } }
                        .buttonStyle(.bordered)
                        .disabled(isServiceBusy || configStore.inboxPath.isEmpty)
                    Button("Logs") { Task { await loadServiceLogs() } }
                        .buttonStyle(.bordered)
                        .disabled(isServiceBusy || configStore.inboxPath.isEmpty)
                    Button("Uninstall") { Task { await runService("uninstall") } }
                        .buttonStyle(.bordered)
                        .disabled(isServiceBusy || configStore.inboxPath.isEmpty)
                }
                Text("Install/start/stop are explicit CLI actions. The app does not silently install a persistent watcher.")
                    .font(.caption)
                    .foregroundStyle(Theme.textMuted)
                if isServiceBusy {
                    ProgressView("Running service command...")
                        .controlSize(.small)
                } else if let serviceResult {
                    ScrollView {
                        Text(serviceResult)
                            .font(.caption.monospaced())
                            .foregroundStyle(Theme.textSecondary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .frame(maxHeight: 110)
                    .padding(8)
                    .background(Color.black.opacity(0.35))
                    .clipShape(RoundedRectangle(cornerRadius: 6))
                }
            }
            .padding()
            .background(Theme.bgCard)
            .clipShape(RoundedRectangle(cornerRadius: 12))
            .overlay(
                RoundedRectangle(cornerRadius: 12)
                    .stroke(Theme.borderGlass, lineWidth: 1)
            )

            // Search indexing controls
            VStack(alignment: .leading, spacing: 10) {
                HStack {
                    Label("Search Index", systemImage: "magnifyingglass.circle.fill")
                        .font(.headline)
                        .foregroundStyle(Theme.textPrimary)
                    Spacer()
                    Button("Index Vault") { Task { await indexVault() } }
                        .buttonStyle(.borderedProminent)
                        .tint(Theme.goldPrimary)
                        .disabled(isSearchBusy || configStore.vault.isEmpty)
                }
                Text(configStore.symseekEnabled ? "Post-ingest symseek indexing is enabled in Settings." : "Post-ingest indexing is disabled. Manual vault indexing remains available.")
                    .font(.caption)
                    .foregroundStyle(Theme.textMuted)
                if isSearchBusy {
                    ProgressView("Indexing vault with symseek...")
                        .controlSize(.small)
                } else if let searchResult {
                    ScrollView {
                        Text(searchResult)
                            .font(.caption.monospaced())
                            .foregroundStyle(Theme.textSecondary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .frame(maxHeight: 95)
                    .padding(8)
                    .background(Color.black.opacity(0.35))
                    .clipShape(RoundedRectangle(cornerRadius: 6))
                }
            }
            .padding()
            .background(Theme.bgCard)
            .clipShape(RoundedRectangle(cornerRadius: 12))
            .overlay(
                RoundedRectangle(cornerRadius: 12)
                    .stroke(Theme.borderGlass, lineWidth: 1)
            )
            
            // Drop Zone and Manual Ingestion
            HStack(spacing: 20) {
                // Drop zone
                VStack(spacing: 16) {
                    Image(systemName: isIngesting ? "arrow.triangle.2.circlepath" : "doc.badge.plus")
                        .font(.system(size: 40))
                        .foregroundStyle(isTargeted ? Theme.goldSecondary : Theme.goldPrimary)
                        .symbolEffect(.pulse, isActive: isIngesting)
                    
                    VStack(spacing: 6) {
                        Text(isIngesting ? "Ingesting Document..." : "Drag & Drop Files Here")
                            .font(.headline)
                            .foregroundStyle(Theme.textPrimary)
                        Text("Accepts PDF, images, HEIC/HEIF, TXT, CSV, Markdown, HTML, RTF, DOCX, XLSX, ODT, EML")
                            .font(.caption)
                            .foregroundStyle(Theme.textSecondary)
                    }
                }
                .frame(maxWidth: .infinity, minHeight: 180)
                .background(isTargeted ? Theme.bgCardHover : Theme.bgCard)
                .clipShape(RoundedRectangle(cornerRadius: 12))
                .overlay(
                    RoundedRectangle(cornerRadius: 12)
                        .stroke(
                            isTargeted ? Theme.goldPrimary : Theme.goldPrimary.opacity(0.3),
                            style: StrokeStyle(lineWidth: 1.5, dash: [6, 4])
                        )
                )
                .onDrop(of: [UTType.fileURL], isTargeted: $isTargeted) { providers in
                    guard let item = providers.first else { return false }
                    item.loadItem(forTypeIdentifier: UTType.fileURL.identifier, options: nil) { data, error in
                        if let error = error {
                            print("Drop error: \(error)")
                            return
                        }
                        if let urlData = data as? Data,
                           let url = URL(dataRepresentation: urlData, relativeTo: nil) {
                            Task { @MainActor in
                                await ingestFile(at: url)
                            }
                        } else if let url = data as? URL {
                            Task { @MainActor in
                                await ingestFile(at: url)
                            }
                        }
                    }
                    return true
                }
                
                // Details / Result of manual ingest
                VStack(alignment: .leading, spacing: 12) {
                    Text("Manual Ingestion Result")
                        .font(.headline)
                        .foregroundStyle(Theme.textPrimary)
                    
                    if isIngesting {
                        ProgressView("Running symingest pipeline...")
                            .progressViewStyle(.circular)
                            .foregroundStyle(Theme.textSecondary)
                    } else if let result = ingestResult {
                        ScrollView {
                            Text(result)
                                .font(.caption.monospaced())
                                .foregroundStyle(isResultSuccess ? Theme.textPrimary : .red)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .padding(8)
                        .background(Color.black.opacity(0.3))
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                    } else {
                        ContentUnavailableView("No active run", systemImage: "doc.text.magnifyingglass")
                            .font(.caption)
                            .foregroundStyle(Theme.textMuted)
                    }
                }
                .padding()
                .frame(width: 320, height: 180)
                .background(Theme.bgCard)
                .clipShape(RoundedRectangle(cornerRadius: 12))
                .overlay(
                    RoundedRectangle(cornerRadius: 12)
                        .stroke(Theme.borderGlass, lineWidth: 1)
                )
            }
            
            // Watcher Logs Console
            VStack(alignment: .leading, spacing: 10) {
                HStack {
                    Label("Watcher Log Console", systemImage: "terminal.fill")
                        .font(.headline)
                        .foregroundStyle(Theme.textPrimary)
                    Spacer()
                    if !engineManager.logs.isEmpty {
                        Button("Clear Console") {
                            // We don't have clear on EngineManager but we can stop/start or just clear view state
                        }
                        .buttonStyle(.plain)
                        .foregroundStyle(Theme.goldPrimary)
                        .font(.caption)
                    }
                }
                
                ScrollViewReader { proxy in
                    ScrollView {
                        LazyVStack(alignment: .leading, spacing: 4) {
                            if engineManager.logs.isEmpty {
                                Text("No logs. Start the watcher or drop a file to see output.")
                                    .foregroundStyle(Theme.textMuted)
                                    .font(.caption.monospaced())
                            } else {
                                ForEach(engineManager.logs.indices, id: \.self) { idx in
                                    Text(engineManager.logs[idx])
                                        .font(.system(.caption, design: .monospaced))
                                        .foregroundStyle(Theme.textSecondary)
                                        .id(idx)
                                }
                            }
                        }
                        .padding(12)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background(Color.black.opacity(0.55))
                    .clipShape(RoundedRectangle(cornerRadius: 8))
                    .overlay(
                        RoundedRectangle(cornerRadius: 8)
                            .stroke(Theme.borderGlass, lineWidth: 1)
                    )
                    .onChange(of: engineManager.logs.count) {
                        if let lastIdx = engineManager.logs.indices.last {
                            withAnimation {
                                proxy.scrollTo(lastIdx, anchor: .bottom)
                            }
                        }
                    }
                }
            }
            .frame(maxHeight: .infinity)
        }
    }
    
    private var statusColor: Color {
        switch engineManager.state {
        case .stopped: return .gray
        case .starting: return .yellow
        case .running: return .green
        case .failed: return .red
        }
    }
    
    private var statusText: String {
        switch engineManager.state {
        case .stopped: return "Stopped"
        case .starting: return "Starting..."
        case .running: return "Active"
        case .failed(let err): return "Failed (\(err))"
        }
    }
    
    private func ingestFile(at url: URL) async {
        isIngesting = true
        ingestResult = nil
        
        let path = url.path
        appendLogToConsole("[manual] Ingesting manual drop: \(path)")
        
        let (success, message) = await CLIClient.shared.ingestFile(filePath: path, config: configStore)
        
        isIngesting = false
        isResultSuccess = success
        ingestResult = message
        
        appendLogToConsole("[manual] Ingestion \(success ? "completed" : "failed"): \(message)")
    }
    
    private func appendLogToConsole(_ text: String) {
        // Since we cannot write directly to engineManager logs array because it is private(set),
        // we can just print it. But we could also add helper in EngineManager if we want logs to display there.
    }

    private func runService(_ command: String, dryRun: Bool = false, json: Bool = false) async {
        isServiceBusy = true
        serviceResult = nil
        let result = await CLIClient.shared.service(command: command, dryRun: dryRun, json: json, config: configStore)
        isServiceBusy = false
        serviceResult = result.message.isEmpty ? (result.success ? "OK" : "Failed") : result.message
    }

    private func loadServiceLogs() async {
        isServiceBusy = true
        serviceResult = nil
        let result = await CLIClient.shared.serviceLogs(config: configStore)
        isServiceBusy = false
        serviceResult = result.message.isEmpty ? "No service logs yet." : result.message
    }

    private func indexVault() async {
        isSearchBusy = true
        searchResult = nil
        let result = await CLIClient.shared.indexVault(config: configStore)
        isSearchBusy = false
        searchResult = result.message.isEmpty ? (result.success ? "Vault indexed." : "Indexing failed.") : result.message
    }
}
