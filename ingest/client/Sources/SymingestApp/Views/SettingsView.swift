import SwiftUI

struct SettingsView: View {
    @Environment(ConfigStore.self) private var configStore
    
    // Doctor status
    @State private var report: DependencyReport?
    @State private var checking = false
    @State private var saveMessage: String?
    
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            // Header
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Settings & Doctor")
                        .font(Font.title.bold())
                        .foregroundStyle(Theme.textPrimary)
                    Text("Configure your directories and check system dependencies.")
                        .font(.subheadline)
                        .foregroundStyle(Theme.textSecondary)
                }
                Spacer()
            }
            
            HStack(alignment: .top, spacing: 24) {
                // Settings Form (Left Panel)
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        Text("Application Settings")
                            .font(.headline)
                            .foregroundStyle(Theme.goldPrimary)
                        
                        // Vault
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Vault Directory (Markdown Output)")
                                .font(.caption)
                                .foregroundStyle(Theme.textSecondary)
                            HStack {
                                TextField("Choose path to your markdown notes folder", text: Bindable(configStore).vault)
                                    .textFieldStyle(.roundedBorder)
                                Button("Choose...") {
                                    chooseDirectory(for: Bindable(configStore).vault)
                                }
                            }
                        }
                        
                        // Inbox
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Inbox Directory (Folder to Watch)")
                                .font(.caption)
                                .foregroundStyle(Theme.textSecondary)
                            HStack {
                                TextField("Choose path to incoming scans inbox", text: Bindable(configStore).inboxPath)
                                    .textFieldStyle(.roundedBorder)
                                Button("Choose...") {
                                    chooseDirectory(for: Bindable(configStore).inboxPath)
                                }
                            }
                        }
                        
                        // Archive
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Archive Directory (Original Attachments)")
                                .font(.caption)
                                .foregroundStyle(Theme.textSecondary)
                            HStack {
                                TextField("Choose path to store original PDFs/images", text: Bindable(configStore).archivePath)
                                    .textFieldStyle(.roundedBorder)
                                Button("Choose...") {
                                    chooseDirectory(for: Bindable(configStore).archivePath)
                                }
                            }
                        }
                        
                        // DB
                        VStack(alignment: .leading, spacing: 4) {
                            Text("SQLite Database Path")
                                .font(.caption)
                                .foregroundStyle(Theme.textSecondary)
                            HStack {
                                TextField("Choose path to jobs queue database file", text: Bindable(configStore).dbPath)
                                    .textFieldStyle(.roundedBorder)
                                Button("Choose...") {
                                    chooseFile(for: Bindable(configStore).dbPath)
                                }
                            }
                        }
                        
                        // Language
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Tesseract OCR Language Override")
                                .font(.caption)
                                .foregroundStyle(Theme.textSecondary)
                            TextField("e.g. eng, deu, eng+deu (default: eng)", text: Bindable(configStore).ocrLang)
                                .textFieldStyle(.roundedBorder)
                        }
                        
                        // Custom Binary Path
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Custom symingest Binary Path (Optional)")
                                .font(.caption)
                                .foregroundStyle(Theme.textSecondary)
                            HStack {
                                TextField("Leave blank to use bundled resource or system PATH", text: Bindable(configStore).customBinaryPath)
                                    .textFieldStyle(.roundedBorder)
                                Button("Choose...") {
                                    chooseFile(for: Bindable(configStore).customBinaryPath)
                                }
                            }
                        }

                        // Symseek
                        VStack(alignment: .leading, spacing: 8) {
                            Toggle("Index new ingests with symseek", isOn: Bindable(configStore).symseekEnabled)
                                .toggleStyle(.checkbox)
                            Text("Optional: after a successful ingest, symingest asks symseek to index the generated Markdown note. Ingest does not fail if search indexing fails.")
                                .font(.caption)
                                .foregroundStyle(Theme.textMuted)
                            HStack {
                                TextField("Custom symseek binary path (optional)", text: Bindable(configStore).symseekBinary)
                                    .textFieldStyle(.roundedBorder)
                                Button("Choose...") {
                                    chooseFile(for: Bindable(configStore).symseekBinary)
                                }
                            }
                        }
                        
                        // Save Button
                        HStack {
                            Button("Save Configuration") {
                                configStore.save()
                                showSaveMessage("Configuration saved successfully!")
                                Task {
                                    await checkDependencies()
                                }
                            }
                            .buttonStyle(.borderedProminent)
                            .tint(Theme.goldPrimary)
                            .foregroundStyle(Theme.bgDark)
                            .font(.headline)
                            
                            if let msg = saveMessage {
                                Text(msg)
                                    .font(.caption)
                                    .foregroundStyle(.green)
                                    .transition(.opacity)
                            }
                        }
                        .padding(.top, 8)
                    }
                    .padding(.trailing, 10)
                }
                .frame(maxWidth: .infinity)
                
                // Doctor Diagnostics (Right Panel)
                VStack(alignment: .leading, spacing: 16) {
                    HStack {
                        Text("System Doctor Diagnostics")
                            .font(.headline)
                            .foregroundStyle(Theme.goldPrimary)
                        Spacer()
                        Button {
                            Task {
                                await checkDependencies()
                            }
                        } label: {
                            Image(systemName: "arrow.clockwise")
                        }
                        .buttonStyle(.plain)
                        .disabled(checking)
                    }
                    
                    VStack(alignment: .leading, spacing: 12) {
                        // Diagnostic Card
                        if checking {
                            ProgressView("Running diagnostics...")
                                .padding()
                        } else if let report = report {
                            VStack(alignment: .leading, spacing: 14) {
                                // Diagnostic status banner
                                HStack {
                                    Image(systemName: report.isComplete ? "checkmark.seal.fill" : "exclamationmark.triangle.fill")
                                        .foregroundStyle(report.isComplete ? .green : .orange)
                                        .font(.title2)
                                    Text(report.isComplete ? "System Healthy" : "Missing Dependencies")
                                        .font(.headline)
                                        .foregroundStyle(Theme.textPrimary)
                                }
                                .padding(.bottom, 6)
                                
                                // symingest
                                dependencyRow(
                                    name: "symingest CLI",
                                    path: report.symingestPath,
                                    help: "Required. Compile with 'go build ./cmd/symingest' or place in PATH."
                                )
                                
                                // tesseract
                                dependencyRow(
                                    name: "tesseract OCR",
                                    path: report.tesseractPath,
                                    help: "Required for image/PDF OCR. Install via Homebrew: 'brew install tesseract'."
                                )
                                
                                // pdftoppm
                                dependencyRow(
                                    name: "pdftoppm (poppler)",
                                    path: report.pdftoppmPath,
                                    help: "Required for PDF parsing. Install via Homebrew: 'brew install poppler'."
                                )
                                
                                // sips
                                dependencyRow(
                                    name: "sips utility",
                                    path: report.sipsPath,
                                    help: "Optional. Pre-installed tool on macOS for processing HEIC/HEIF scans.",
                                    isOptional: true
                                )

                                dependencyRow(
                                    name: "textutil",
                                    path: report.textutilPath,
                                    help: "Optional fallback converter for rich text / office-like documents on macOS.",
                                    isOptional: true
                                )

                                dependencyRow(
                                    name: "pandoc",
                                    path: report.pandocPath,
                                    help: "Optional fallback converter for document formats. Install via Homebrew: 'brew install pandoc'.",
                                    isOptional: true
                                )

                                dependencyRow(
                                    name: "LibreOffice",
                                    path: report.libreOfficePath ?? report.sofficePath,
                                    help: "Optional fallback converter for office documents. Install via Homebrew Cask: 'brew install --cask libreoffice'.",
                                    isOptional: true
                                )
                            }
                        }
                    }
                    .padding()
                    .background(Theme.bgCard)
                    .clipShape(RoundedRectangle(cornerRadius: 12))
                    .overlay(
                        RoundedRectangle(cornerRadius: 12)
                            .stroke(Theme.borderGlass, lineWidth: 1)
                    )
                }
                .frame(width: 360)
            }
        }
        .onAppear {
            Task {
                await checkDependencies()
            }
        }
    }
    
    private func checkDependencies() async {
        checking = true
        report = await CLIClient.shared.checkDependencies(customPath: configStore.customBinaryPath)
        checking = false
    }
    
    private func showSaveMessage(_ text: String) {
        saveMessage = text
        Task {
            try? await Task.sleep(for: .seconds(2))
            saveMessage = nil
        }
    }
    
    private func chooseDirectory(for path: Binding<String>) {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        if panel.runModal() == .OK {
            if let url = panel.url {
                path.wrappedValue = url.path
            }
        }
    }
    
    private func chooseFile(for path: Binding<String>) {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false
        if panel.runModal() == .OK {
            if let url = panel.url {
                path.wrappedValue = url.path
            }
        }
    }
    
    @ViewBuilder
    private func dependencyRow(name: String, path: String?, help: String, isOptional: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                Image(systemName: path != nil ? "checkmark.circle.fill" : (isOptional ? "questionmark.circle.fill" : "xmark.circle.fill"))
                    .foregroundStyle(path != nil ? .green : (isOptional ? .orange : .red))
                
                Text(name)
                    .font(.body.bold())
                    .foregroundStyle(Theme.textPrimary)
            }
            
            if let resolvedPath = path {
                Text(resolvedPath)
                    .font(.caption.monospaced())
                    .foregroundStyle(Theme.textMuted)
                    .lineLimit(1)
                    .truncationMode(.middle)
            } else {
                Text(help)
                    .font(.caption)
                    .foregroundStyle(isOptional ? Theme.textSecondary : Color.red.opacity(0.8))
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}
