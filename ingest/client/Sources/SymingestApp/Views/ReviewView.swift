import SwiftUI
import AppKit
import UniformTypeIdentifiers

struct ReviewView: View {
    @Environment(ConfigStore.self) private var configStore

    @State private var reportPath = ""
    @State private var htmlPath = ""
    @State private var correctionsPath = ""
    @State private var backupDir = ""
    @State private var requireCountText = ""
    @State private var maxText = ""

    @State private var failed = true
    @State private var warnings = true
    @State private var missingMetadata = false
    @State private var lowBody = false
    @State private var duplicateContent = false
    @State private var unsupported = true
    @State private var unresolved = true

    @State private var isLoading = false
    @State private var reviewReport: ReviewReportDTO?
    @State private var message: String?
    @State private var correctionOutput = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Migration Review")
                        .font(Font.title.bold())
                        .foregroundStyle(Theme.textPrimary)
                    Text("Load migration/verify reports, inspect findings, and apply schema-versioned corrections safely.")
                        .font(.subheadline)
                        .foregroundStyle(Theme.textSecondary)
                }
                Spacer()
            }

            HStack(alignment: .top, spacing: 20) {
                controlPanel
                    .frame(width: 340)

                VStack(alignment: .leading, spacing: 12) {
                    if let message {
                        Text(message)
                            .font(.caption)
                            .foregroundStyle(message.lowercased().contains("failed") ? .red : Theme.goldSecondary)
                            .padding(8)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(Theme.bgCard)
                            .clipShape(RoundedRectangle(cornerRadius: 8))
                    }
                    reviewResults
                    if !correctionOutput.isEmpty {
                        Text("Correction Output")
                            .font(.headline)
                            .foregroundStyle(Theme.textPrimary)
                        ScrollView {
                            Text(correctionOutput)
                                .font(.caption.monospaced())
                                .foregroundStyle(Theme.textSecondary)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .frame(height: 120)
                        .padding(8)
                        .background(Color.black.opacity(0.45))
                        .clipShape(RoundedRectangle(cornerRadius: 8))
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
    }

    private var controlPanel: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                GroupBox("Report") {
                    VStack(alignment: .leading, spacing: 8) {
                        HStack {
                            TextField("migration/verify report JSON", text: $reportPath)
                                .textFieldStyle(.roundedBorder)
                            Button("Choose") { chooseFile(into: $reportPath, allowed: ["json"]) }
                        }
                        Toggle("Failed", isOn: $failed)
                        Toggle("Warnings", isOn: $warnings)
                        Toggle("Missing metadata", isOn: $missingMetadata)
                        Toggle("Low body", isOn: $lowBody)
                        Toggle("Duplicate content", isOn: $duplicateContent)
                        Toggle("Unsupported", isOn: $unsupported)
                        Toggle("Unresolved refs", isOn: $unresolved)

                        Button {
                            Task { await loadReview() }
                        } label: {
                            Label(isLoading ? "Loading..." : "Load Review", systemImage: "doc.text.magnifyingglass")
                        }
                        .buttonStyle(.borderedProminent)
                        .tint(Theme.goldPrimary)
                        .foregroundStyle(Theme.bgDark)
                        .disabled(reportPath.isEmpty || isLoading)

                        HStack {
                            TextField("HTML output path", text: $htmlPath)
                                .textFieldStyle(.roundedBorder)
                            Button("Save As") { chooseSaveHTML() }
                        }
                        Button("Write HTML Review") {
                            Task { await writeHTML() }
                        }
                        .buttonStyle(.bordered)
                        .disabled(reportPath.isEmpty || htmlPath.isEmpty || isLoading)
                    }
                    .padding(.vertical, 4)
                }

                GroupBox("Corrections") {
                    VStack(alignment: .leading, spacing: 8) {
                        HStack {
                            TextField("corrections.yaml", text: $correctionsPath)
                                .textFieldStyle(.roundedBorder)
                            Button("Choose") { chooseFile(into: $correctionsPath, allowed: ["yaml", "yml"]) }
                        }
                        TextField("Backup dir for final apply", text: $backupDir)
                            .textFieldStyle(.roundedBorder)
                        HStack {
                            TextField("require-count", text: $requireCountText)
                                .textFieldStyle(.roundedBorder)
                            TextField("max", text: $maxText)
                                .textFieldStyle(.roundedBorder)
                        }
                        Text("Dry-run first. Final apply writes undo backups and refuses surprise counts.")
                            .font(.caption)
                            .foregroundStyle(Theme.textMuted)

                        HStack {
                            Button("Dry-run Corrections") {
                                Task { await applyCorrections(dryRun: true) }
                            }
                            .buttonStyle(.bordered)
                            .disabled(correctionsPath.isEmpty || configStore.vault.isEmpty || isLoading)

                            Button("Final Apply") {
                                Task { await applyCorrections(dryRun: false) }
                            }
                            .buttonStyle(.borderedProminent)
                            .tint(.orange)
                            .disabled(correctionsPath.isEmpty || configStore.vault.isEmpty || isLoading)
                        }
                    }
                    .padding(.vertical, 4)
                }
            }
        }
    }

    private var reviewResults: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Findings")
                    .font(.headline)
                    .foregroundStyle(Theme.textPrimary)
                Spacer()
                if let reviewReport {
                    Text("\(reviewReport.sourceKind) · \(reviewReport.total) documents")
                        .font(.caption.monospaced())
                        .foregroundStyle(Theme.textMuted)
                }
            }

            if isLoading {
                ProgressView("Running symingest review-report...")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let report = reviewReport {
                List {
                    if let findings = report.findings, !findings.isEmpty {
                        Section("Report Findings") {
                            ForEach(findings) { finding in
                                HStack(alignment: .top) {
                                    Text(finding.kind)
                                        .font(.caption.monospaced().bold())
                                        .foregroundStyle(.orange)
                                        .frame(width: 160, alignment: .leading)
                                    Text(finding.documentID.map(String.init) ?? "—")
                                        .font(.caption.monospaced())
                                        .frame(width: 50, alignment: .leading)
                                    Text(finding.message)
                                        .font(.caption)
                                        .foregroundStyle(Theme.textSecondary)
                                }
                            }
                        }
                    }
                    Section("Documents") {
                        ForEach(report.documents) { doc in
                            VStack(alignment: .leading, spacing: 4) {
                                HStack {
                                    Text("#\(doc.id)")
                                        .font(.caption.monospaced().bold())
                                    Text(doc.status)
                                        .font(.caption.monospaced())
                                        .foregroundStyle(statusColor(doc.status))
                                    Spacer()
                                    Text((doc.findings ?? []).joined(separator: ", "))
                                        .font(.caption2)
                                        .foregroundStyle(Theme.textMuted)
                                }
                                if let error = doc.error, !error.isEmpty {
                                    Text(error).font(.caption).foregroundStyle(.red)
                                }
                                if let warnings = doc.warnings, !warnings.isEmpty {
                                    Text(warnings.joined(separator: " · ")).font(.caption).foregroundStyle(.orange)
                                }
                                HStack {
                                    pathButton("Vault", path: doc.vaultPath)
                                    pathButton("Archive", path: doc.archivePath)
                                }
                            }
                            .padding(.vertical, 4)
                        }
                    }
                }
                .listStyle(.inset)
                .background(Color.clear)
            } else {
                ContentUnavailableView("No review loaded", systemImage: "doc.text.magnifyingglass", description: Text("Choose a migration or verify report and load a filtered review."))
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
    }

    private func selectedFilters() -> [String] {
        var filters: [String] = []
        if failed { filters.append("--failed") }
        if warnings { filters.append("--warnings") }
        if missingMetadata { filters.append("--missing-metadata") }
        if lowBody { filters.append("--low-body") }
        if duplicateContent { filters.append("--duplicate-content") }
        if unsupported { filters.append("--unsupported") }
        if unresolved { filters.append("--unresolved") }
        return filters
    }

    private func loadReview() async {
        isLoading = true
        message = nil
        do {
            reviewReport = try await CLIClient.shared.buildReviewReport(reportPath: reportPath, filters: selectedFilters(), config: configStore)
            message = "Review loaded. Body text is not included."
        } catch {
            message = "Review failed: \(error.localizedDescription)"
        }
        isLoading = false
    }

    private func writeHTML() async {
        isLoading = true
        message = nil
        do {
            message = try await CLIClient.shared.writeReviewHTML(reportPath: reportPath, htmlPath: htmlPath, filters: selectedFilters(), config: configStore)
            NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: htmlPath)])
        } catch {
            message = "HTML review failed: \(error.localizedDescription)"
        }
        isLoading = false
    }

    private func applyCorrections(dryRun: Bool) async {
        isLoading = true
        message = nil
        do {
            correctionOutput = try await CLIClient.shared.applyCorrections(
                vault: configStore.vault,
                correctionsPath: correctionsPath,
                dryRun: dryRun,
                requireCount: Int(requireCountText),
                max: Int(maxText),
                backupDir: backupDir,
                config: configStore
            )
            message = dryRun ? "Correction dry-run completed." : "Corrections applied with undo backups."
        } catch {
            message = "Corrections failed: \(error.localizedDescription)"
        }
        isLoading = false
    }

    private func chooseFile(into binding: Binding<String>, allowed: [String]) {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = false
        panel.canChooseFiles = true
        panel.allowsMultipleSelection = false
        panel.allowedContentTypes = allowed.compactMap { UTType(filenameExtension: $0) }
        if panel.runModal() == .OK, let url = panel.url {
            binding.wrappedValue = url.path
        }
    }

    private func chooseSaveHTML() {
        let panel = NSSavePanel()
        panel.allowedContentTypes = [UTType.html]
        panel.nameFieldStringValue = "symingest-review.html"
        if panel.runModal() == .OK, let url = panel.url {
            htmlPath = url.path
        }
    }

    private func pathButton(_ title: String, path: String?) -> some View {
        Button(title) {
            if let path, !path.isEmpty {
                NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)])
            }
        }
        .disabled((path ?? "").isEmpty)
        .buttonStyle(.borderless)
        .font(.caption)
    }

    private func statusColor(_ status: String) -> Color {
        switch status.lowercased() {
        case "failed", "missing", "missing_archive", "hash_mismatch", "source_hash_mismatch", "metadata_mismatch": return .red
        case "duplicate_content", "warning": return .orange
        case "imported", "verified": return .green
        default: return Theme.textSecondary
        }
    }
}
