import SwiftUI
import SymairaTheme
import SymDeskCore

// MARK: - CompanionToolsView

enum ToolInstallState {
    case installed
    case notInstalled
    case unknown
}

struct CompanionToolsView: View {
    @EnvironmentObject var core: DeskCore

    let doctorReport: DoctorReport?
    let onDoctorRefresh: @Sendable () async -> Void

    @State private var installingTools: Set<String> = []
    @State private var installOutput: [String: String] = [:]
    @State private var installErrors: [String: String] = [:]
    @State private var homebrewAvailable: Bool?

    private let managedTools: [(id: String, name: String, tap: String)] = [
        ("symseek", "SymSeek", "danieljustus/tap/symseek"),
        ("symmemory", "SymMemory", "danieljustus/tap/symmemory"),
        ("symingest", "SymIngest", "danieljustus/tap/symingest"),
    ]

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                headerSection

                if let hb = homebrewAvailable, !hb {
                    noHomebrewWarning
                }

                toolsSection

                if homebrewAvailable == nil {
                    ProgressView("Checking Homebrew…")
                        .tint(SymairaTheme.goldPrimary)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 20)
                }
            }
            .padding(28)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .navigationTitle("Companion Tools")
        .task {
            homebrewAvailable = checkHomebrew()
        }
    }

    // MARK: - Header

    private var headerSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Companion Tools")
                .symairaText(.title).bold()
                .foregroundColor(SymairaTheme.textPrimary)

            Text("Optional tools that extend SymDesk with search, memory, and OCR capabilities. Install them with one click.")
                .symairaText(.callout)
                .foregroundColor(SymairaTheme.textSecondary)
        }
    }

    private var noHomebrewWarning: some View {
        HStack(spacing: 12) {
            Image(systemName: "exclamationmark.triangle.fill")
                .symairaText(.heading)
                .foregroundColor(.orange)
            VStack(alignment: .leading, spacing: 4) {
                Text("Homebrew not found")
                    .symairaText(.body).fontWeight(.semibold)
                    .foregroundColor(SymairaTheme.textPrimary)
                Text("Homebrew is required to install companion tools. Install it from [brew.sh](https://brew.sh) and restart SymDesk.")
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textSecondary)
            }
        }
        .padding(14)
        .glassCard()
    }

    // MARK: - Tools

    private var toolsSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Available Tools")
                .symairaText(.subheading)
                .foregroundColor(SymairaTheme.goldPrimary)

            ForEach(managedTools, id: \.id) { tool in
                toolRow(tool)
            }
        }
    }

    private func toolRow(_ tool: (id: String, name: String, tap: String)) -> some View {
        let installState: ToolInstallState = {
            guard let report = doctorReport else { return .unknown }
            return report.tools.isAvailable(tool.id) ? .installed : .notInstalled
        }()
        let isInstalled = installState == .installed
        let isUnknown = installState == .unknown
        let isInstalling = installingTools.contains(tool.id)
        let version = doctorReport?.versions?[tool.id]
        let output = installOutput[tool.id]
        let error = installErrors[tool.id]

        return VStack(alignment: .leading, spacing: 8) {
            HStack {
                Image(systemName: isInstalled ? "checkmark.seal.fill" : (isInstalling ? "arrow.down.circle" : "questionmark.circle"))
                    .symairaText(.heading)
                    .foregroundColor(isInstalled ? .green : (isInstalling ? SymairaTheme.goldPrimary : SymairaTheme.textMuted))

                VStack(alignment: .leading, spacing: 2) {
                    Text(tool.name)
                        .symairaText(.body).fontWeight(.semibold)
                        .foregroundColor(SymairaTheme.textPrimary)
                    if let version {
                        Text("v\(version)")
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textSecondary)
                    } else if isUnknown {
                        Text("could not read the vault health report")
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textMuted)
                    } else if !isInstalled && !isInstalling {
                        Text("Not installed")
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textMuted)
                    }
                }

                Spacer()

                if isInstalled {
                    Label("Installed", systemImage: "checkmark")
                        .symairaText(.caption)
                        .foregroundColor(.green)
                } else if isInstalling {
                    ProgressView()
                        .controlSize(.small)
                } else {
                    Button("Install") {
                        installTool(tool)
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .tint(SymairaTheme.goldPrimary)
                    .disabled(homebrewAvailable != true || isUnknown)
                }
            }

            if isInstalling, let output {
                Text(output)
                    .symairaText(.caption).monospaced()
                    .foregroundColor(SymairaTheme.textMuted)
                    .lineLimit(4)
            }

            if let error {
                Text(error)
                    .symairaText(.caption)
                    .foregroundColor(.red)
            }
        }
        .padding(14)
        .glassCard()
    }

    // MARK: - Homebrew check

    private func checkHomebrew() -> Bool {
        let paths = ["/opt/homebrew/bin/brew", "/usr/local/bin/brew"]
        for path in paths {
            if FileManager.default.isExecutableFile(atPath: path) {
                return true
            }
        }
        // Also check via which
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        task.arguments = ["which", "brew"]
        let pipe = Pipe()
        task.standardOutput = pipe
        task.standardError = FileHandle.nullDevice
        do {
            try task.run()
            task.waitUntilExit()
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            let result = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            return !result.isEmpty && task.terminationStatus == 0
        } catch {
            return false
        }
    }

    // MARK: - Install tool

    private func installTool(_ tool: (id: String, name: String, tap: String)) {
        installingTools.insert(tool.id)
        installOutput[tool.id] = "Installing \(tool.name)…"
        installErrors[tool.id] = nil

        Task.detached(priority: .background) {
            let brewPath = findBrewPath()

            let task = Process()
            task.executableURL = URL(fileURLWithPath: brewPath)
            task.arguments = ["install", tool.tap]
            task.environment = ["HOME": NSHomeDirectory(), "PATH": resolvedInstallerPATH()]

            let pipe = Pipe()
            task.standardOutput = pipe
            task.standardError = pipe

            do {
                try task.run()
                task.waitUntilExit()

                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                let output = String(data: data, encoding: .utf8) ?? ""

                if task.terminationStatus == 0 {
                    await MainActor.run {
                        installOutput[tool.id] = "Installation complete."
                        installingTools.remove(tool.id)
                    }
                    // Re-run doctor to update status
                    await onDoctorRefresh()
                } else {
                    let errMsg = output.prefix(200)
                    await MainActor.run {
                        installErrors[tool.id] = "Install failed: \(errMsg)"
                        installingTools.remove(tool.id)
                    }
                }
            } catch {
                await MainActor.run {
                    installErrors[tool.id] = "Install failed: \(error.localizedDescription)"
                    installingTools.remove(tool.id)
                }
            }
        }
    }

    private nonisolated func findBrewPath() -> String {
        for path in ["/opt/homebrew/bin/brew", "/usr/local/bin/brew"] {
            if FileManager.default.isExecutableFile(atPath: path) {
                return path
            }
        }
        return "/opt/homebrew/bin/brew"
    }

    /// Builds the PATH given to the `brew install`/probe subprocess using the
    /// same resolution order as the Go core's compose.Resolve (issue #463):
    /// $SYMAIRA_BIN, then the managed runtime directory (`~/.symaira/bin`),
    /// ahead of the existing Homebrew/system fallback directories. A
    /// GUI-launched app inherits launchd's minimal PATH, so without this the
    /// subprocess could not see a managed-runtime-only install even though
    /// the doctor report (which already checks these directories) says it's
    /// there.
    private nonisolated func resolvedInstallerPATH() -> String {
        var components: [String] = []
        if let symairaBin = ProcessInfo.processInfo.environment["SYMAIRA_BIN"], !symairaBin.isEmpty {
            components.append(symairaBin)
        }
        components.append("\(NSHomeDirectory())/.symaira/bin")
        components.append(contentsOf: ["/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"])
        return components.joined(separator: ":")
    }
}
