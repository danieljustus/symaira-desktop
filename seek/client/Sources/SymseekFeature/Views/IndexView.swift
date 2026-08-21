import SwiftUI
import AppKit
import Observation

struct IndexView: View {
    @Bindable var engineManager: EngineManager
    @State private var model = IndexModel()

    var body: some View {
        @Bindable var model = model
        VStack(alignment: .leading, spacing: 20) {
            // Header
            VStack(alignment: .leading, spacing: 4) {
                Text("INDEX MANAGEMENT")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(Color.symairaGold)
                    .tracking(2)
                
                Text("Crawl and Watch Directories")
                    .font(.title2.bold())
                    .foregroundStyle(Color.symairaText)
            }
            .padding([.top, .horizontal])
            
            // Directory Selection Card
            VStack(alignment: .leading, spacing: 16) {
                HStack {
                    Image(systemName: "folder.badge.plus")
                        .font(.title3)
                        .foregroundStyle(Color.symairaGold)
                    Text("Select Folder to Index")
                        .font(.headline)
                        .foregroundStyle(Color.symairaText)
                }
                
                HStack(spacing: 12) {
                    TextField("No folder selected", text: $model.selectedFolder)
                        .textFieldStyle(.plain)
                        .padding(10)
                        .background(Color.symairaBgDarker)
                        .cornerRadius(8)
                        .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color.symairaBorder, lineWidth: 1))
                        .disabled(model.isIndexing)
                    
                    Button("Browse…") {
                        model.selectFolder()
                    }
                    .buttonStyle(.bordered)
                    .disabled(model.isIndexing)
                    .accessibilityLabel("Browse for folder to index")
                }
                
                // Watch Mode Toggle
                Toggle(isOn: $model.watchMode) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Watch for modifications (Daemon mode)")
                            .font(.body)
                            .foregroundStyle(Color.symairaText)
                        Text("Monitors folder and automatically re-indexes changed, new, or deleted files in the background.")
                            .font(.caption)
                            .foregroundStyle(Color.symairaTextSecondary)
                    }
                }
                .disabled(model.isIndexing)
                .padding(.vertical, 4)
                
                Divider()
                    .background(Color.symairaBorder)
                
                // Action Buttons
                HStack {
                    if model.isIndexing {
                        if model.watchMode {
                            HStack(spacing: 8) {
                                Circle()
                                    .fill(Color.green)
                                    .frame(width: 8, height: 8)
                                    .shadow(color: .green, radius: 4)
                                Text("WATCHING FOR CHANGES")
                                    .font(.system(.caption, design: .monospaced))
                                    .foregroundStyle(Color.green)
                                    .bold()
                            }
                        } else {
                            HStack(spacing: 8) {
                                ProgressView()
                                    .controlSize(.small)
                                Text("CRAWLING DIRECTORY…")
                                    .font(.system(.caption, design: .monospaced))
                                    .foregroundStyle(Color.symairaGold)
                                    .bold()
                            }
                        }
                        Spacer()
                        Button("Stop Process") {
                            model.stopIndexing()
                        }
                        .buttonStyle(.borderedProminent)
                        .tint(.red.opacity(0.8))
                        .accessibilityLabel("Stop indexing process")
                    } else {
                        Text("Ready to index.")
                            .font(.system(.caption, design: .monospaced))
                            .foregroundStyle(Color.symairaMuted)
                        Spacer()
                        Button("Start Indexing") {
                            model.startIndexing()
                        }
                        .buttonStyle(.borderedProminent)
                        .tint(Color.symairaGold)
                        .foregroundStyle(Color.symairaBg)
                        .disabled(model.selectedFolder.isEmpty)
                        .accessibilityLabel("Start indexing")
                    }
                }
            }
            .symairaCard()
            .padding(.horizontal)
            
            // Console Logs Card
            VStack(alignment: .leading, spacing: 10) {
                HStack {
                    Text("CONSOLE LOGS")
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(Color.symairaGold)
                        .bold()
                    Spacer()
                    Button("Clear Logs") {
                        model.logs = []
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(Color.symairaMuted)
                    .font(.caption)
                    .accessibilityLabel("Clear console logs")
                }
                
                ScrollViewReader { proxy in
                    ScrollView {
                        VStack(alignment: .leading, spacing: 4) {
                            if model.logs.isEmpty {
                                Text("Console output will stream here…")
                                    .foregroundStyle(Color.symairaMuted)
                                    .font(.system(.caption, design: .monospaced))
                            } else {
                                ForEach(0..<model.logs.count, id: \.self) { index in
                                    Text(model.logs[index])
                                        .foregroundStyle(logColor(for: model.logs[index]))
                                        .font(.system(.caption, design: .monospaced))
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                        .textSelection(.enabled)
                                        .id(index)
                                }
                            }
                        }
                        .padding(12)
                        .frame(maxWidth: .infinity, minHeight: 180, alignment: .topLeading)
                        .background(Color.symairaBgDarker)
                        .cornerRadius(8)
                        .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color.symairaBorder, lineWidth: 1))
                    }
                    .onChange(of: model.logs.count) { _, count in
                        if count > 0 {
                            proxy.scrollTo(count - 1, anchor: .bottom)
                        }
                    }
                }
            }
            .padding([.horizontal, .bottom])
        }
        .background(Color.symairaBg)
        .onDisappear {
            model.stopIndexing()
        }
    }
    
    private func logColor(for line: String) -> Color {
        if line.contains("ERROR") || line.contains("failed") || line.contains("ExitCode") {
            return .red
        }
        if line.contains("WARNING") {
            return .orange
        }
        if line.contains("[gui]") {
            return Color.symairaGold
        }
        return Color.symairaTextSecondary
    }
}

@MainActor
@Observable
class IndexModel {
    var selectedFolder: String = ""
    var watchMode: Bool = false
    var isIndexing: Bool = false
    var logs: [String] = []
    
    // Subprocess state for indexer/watcher
    private var activeProcess: Process?
    private var stdoutFH: FileHandle?
    private var stderrFH: FileHandle?
    
    func selectFolder() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        panel.title = "Select Folder to Index"
        panel.prompt = "Choose"
        
        if panel.runModal() == .OK {
            if let url = panel.url {
                selectedFolder = url.path
            }
        }
    }
    
    func startIndexing() {
        guard !selectedFolder.isEmpty else { return }
        
        logs = []
        isIndexing = true
        appendLog("[gui] Starting indexing process for \(selectedFolder)")
        
        guard let binaryURL = locateBinary() else {
            appendLog("[gui] ERROR: symseek binary not found")
            isIndexing = false
            return
        }
        
        let proc = Process()
        proc.executableURL = binaryURL
        
        var args = ["index", selectedFolder]
        if watchMode {
            args.append("--watch")
            appendLog("[gui] Spawning watch daemon…")
        }
        proc.arguments = args
        
        let stdoutPipe = Pipe()
        let stderrPipe = Pipe()
        proc.standardOutput = stdoutPipe
        proc.standardError = stderrPipe
        
        var env = ProcessInfo.processInfo.environment
        if let path = env["PATH"] {
            env["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:\(path)"
        }
        proc.environment = env
        
        let outFH = stdoutPipe.fileHandleForReading
        let errFH = stderrPipe.fileHandleForReading
        self.stdoutFH = outFH
        self.stderrFH = errFH
        
        outFH.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty, let text = String(data: data, encoding: .utf8) else { return }
            Task { @MainActor [weak self] in
                self?.processLogs(text)
            }
        }
        
        errFH.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty, let text = String(data: data, encoding: .utf8) else { return }
            Task { @MainActor [weak self] in
                self?.processLogs(text)
            }
        }
        
        proc.terminationHandler = { [weak self] completedProc in
            Task { @MainActor [weak self] in
                guard let self else { return }
                let code = completedProc.terminationStatus
                if code == 0 {
                    self.appendLog("[gui] Process finished successfully.")
                } else {
                    self.appendLog("[gui] Process exited with error code \(code)")
                }
                self.cleanup()
                self.isIndexing = false
            }
        }
        
        do {
            try proc.run()
            self.activeProcess = proc
        } catch {
            appendLog("[gui] Failed to launch: \(error.localizedDescription)")
            cleanup()
            isIndexing = false
        }
    }
    
    func stopIndexing() {
        guard let proc = activeProcess, proc.isRunning else { return }
        appendLog("[gui] Stopping indexer process…")
        proc.terminate()
        cleanup()
        isIndexing = false
    }
    
    private func cleanup() {
        stdoutFH?.readabilityHandler = nil
        stderrFH?.readabilityHandler = nil
        stdoutFH = nil
        stderrFH = nil
        activeProcess = nil
    }
    
    private func processLogs(_ text: String) {
        let lines = text.trimmingCharacters(in: .whitespacesAndNewlines).components(separatedBy: .newlines)
        for line in lines {
            let trimmed = line.trimmingCharacters(in: .whitespacesAndNewlines)
            if !trimmed.isEmpty {
                appendLog(trimmed)
            }
        }
    }
    
    private func appendLog(_ text: String) {
        logs.append(text)
        if logs.count > 1000 {
            logs.removeFirst(logs.count - 1000)
        }
    }
    
    private func locateBinary() -> URL? {
        if let bundleURL = Bundle.main.url(forResource: "symseek", withExtension: nil) {
            return bundleURL
        }
        let bundleDir = Bundle.main.bundleURL.deletingLastPathComponent()
        let devBinary = bundleDir.appendingPathComponent("symseek")
        if FileManager.default.fileExists(atPath: devBinary.path) {
            return devBinary
        }
        let projectRoot = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
        let projectBinary = projectRoot.appendingPathComponent("symseek")
        if FileManager.default.fileExists(atPath: projectBinary.path) {
            return projectBinary
        }
        let fallbackPath = "/usr/local/bin/symseek"
        if FileManager.default.fileExists(atPath: fallbackPath) {
            return URL(fileURLWithPath: fallbackPath)
        }
        let armFallbackPath = "/opt/homebrew/bin/symseek"
        if FileManager.default.fileExists(atPath: armFallbackPath) {
            return URL(fileURLWithPath: armFallbackPath)
        }
        return nil
    }
}
