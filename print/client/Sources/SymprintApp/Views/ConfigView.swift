import SwiftUI

struct ConfigView: View {
    @State private var configPath: String = ""
    @State private var configContent: String = ""
    @State private var originalContent: String = ""
    @State private var isLoading = true
    @State private var isSaved = false
    @State private var errorMessage: String?
    
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                HStack {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Configuration File")
                            .font(.title2.bold())
                            .foregroundStyle(Theme.textPrimary)
                        Text("Global symprint config.toml. Changes take effect on next render run.")
                            .font(.subheadline)
                            .foregroundStyle(Theme.textSecondary)
                    }
                    Spacer()
                }
                .padding(.bottom, 8)
                
                if isLoading {
                    HStack {
                        Spacer()
                        ProgressView("Loading configuration…")
                            .foregroundStyle(Theme.textSecondary)
                        Spacer()
                    }
                    .padding(.top, 40)
                } else {
                    VStack(alignment: .leading, spacing: 12) {
                        HStack {
                            Image(systemName: "doc.text.fill")
                                .foregroundStyle(Theme.textMuted)
                            Text(configPath)
                                .font(.system(.caption, design: .monospaced))
                                .foregroundStyle(Theme.textSecondary)
                                .lineLimit(1)
                                .truncationMode(.middle)
                            Spacer()
                            
                            if configContent.isEmpty {
                                Button("Create Default Config") {
                                    Task { await createDefaultConfig() }
                                }
                                .buttonStyle(.borderedProminent)
                                .controlSize(.small)
                            } else {
                                Button("Reset Changes") {
                                    configContent = originalContent
                                }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                                .disabled(configContent == originalContent)
                                
                                Button("Save Configuration") {
                                    saveConfig()
                                }
                                .buttonStyle(.borderedProminent)
                                .controlSize(.small)
                                .disabled(configContent == originalContent)
                            }
                        }
                        
                        if let error = errorMessage {
                            Text(error)
                                .font(.caption)
                                .foregroundStyle(.red)
                        }
                        
                        if isSaved {
                            Text("✓ Configuration saved successfully.")
                                .font(.caption)
                                .foregroundStyle(.green)
                        }
                        
                        if configContent.isEmpty {
                            VStack(spacing: 16) {
                                Image(systemName: "doc.text.magnifyingglass")
                                    .font(.system(size: 48))
                                    .foregroundStyle(Theme.textMuted)
                                Text("No Configuration Found")
                                    .font(.headline)
                                Text("symprint is running with built-in default settings. Click the button above to generate a config.toml file for custom engine/profile configs.")
                                    .font(.caption)
                                    .multilineTextAlignment(.center)
                                    .foregroundStyle(Theme.textSecondary)
                                    .padding(.horizontal, 32)
                            }
                            .padding(.vertical, 40)
                            .frame(maxWidth: .infinity)
                            .glassCard()
                        } else {
                            TextEditor(text: $configContent)
                                .font(.system(.body, design: .monospaced))
                                .frame(minHeight: 450)
                                .padding(8)
                                .background(Theme.bgCard)
                                .cornerRadius(8)
                                .scrollContentBackground(.hidden)
                                .overlay(
                                    RoundedRectangle(cornerRadius: 8)
                                        .stroke(Theme.borderGlass, lineWidth: 1)
                                )
                                .accessibilityLabel("Configuration file contents")
                        }
                    }
                }
            }
            .padding(24)
        }
        .task {
            await loadConfig()
        }
    }
    
    private func loadConfig() async {
        isLoading = true
        configPath = await CliManager.shared.getConfigPath()
        
        let fileURL = URL(fileURLWithPath: configPath)
        if FileManager.default.fileExists(atPath: fileURL.path) {
            do {
                let content = try String(contentsOf: fileURL, encoding: .utf8)
                configContent = content
                originalContent = content
                errorMessage = nil
            } catch {
                errorMessage = "Failed to read configuration: \(error.localizedDescription)"
            }
        } else {
            configContent = ""
            originalContent = ""
        }
        isLoading = false
    }
    
    private func createDefaultConfig() async {
        let success = await CliManager.shared.initializeConfig()
        if success {
            await loadConfig()
            isSaved = true
            Task {
                try? await Task.sleep(for: .seconds(3))
                isSaved = false
            }
        } else {
            errorMessage = "Failed to initialize default configuration."
        }
    }
    
    private func saveConfig() {
        let fileURL = URL(fileURLWithPath: configPath)
        do {
            // Ensure directory exists
            let directoryURL = fileURL.deletingLastPathComponent()
            try FileManager.default.createDirectory(at: directoryURL, withIntermediateDirectories: true)
            
            try configContent.write(to: fileURL, atomically: true, encoding: .utf8)
            originalContent = configContent
            errorMessage = nil
            isSaved = true
            
            Task {
                try? await Task.sleep(for: .seconds(3))
                isSaved = false
            }
        } catch {
            errorMessage = "Failed to write configuration: \(error.localizedDescription)"
        }
    }
}
