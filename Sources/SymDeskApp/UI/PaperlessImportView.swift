import SwiftUI
import AppKit
import SymairaTheme
import SymDeskCore

/// Guided Paperless-ngx import: pick an export directory, preview what would
/// happen (dry-run), then run the import with a progress state and a result
/// summary (issue #307).
///
/// Used in two places: as a sheet from Settings and from the onboarding
/// "ready" step, so the migration path is discoverable exactly where new
/// users are setting up their vault.
struct PaperlessImportView: View {
    @EnvironmentObject var core: DeskCore
    @Environment(\.dismiss) private var dismiss

    @State private var exportDir: String? = nil
    @State private var isPicking = false
    @State private var isRunning = false
    @State private var progressMessage = ""
    @State private var summary: DeskCore.PaperlessImportSummary? = nil
    @State private var errorMessage: String? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 22) {
            header

            // Directory picker
            VStack(alignment: .leading, spacing: 8) {
                LabeledContent("Paperless export") {
                    HStack(spacing: 8) {
                        Text(exportDir ?? "Choose the export directory…")
                            .symairaText(.callout, respectsForeground: false)
                            .foregroundStyle(exportDir == nil ? SymairaTokens.ink2 : SymairaTokens.ink)
                            .lineLimit(1)
                            .truncationMode(.middle)
                            .frame(width: 300, alignment: .leading)
                        Button("Choose…") { pickDirectory() }
                            .symairaButtonStyle(.secondary)
                    }
                }
                Text("A Paperless-ngx export (manifest.json + document files). The import is idempotent — re-running it updates notes instead of duplicating them.")
                    .symairaText(.secondary)
            }
            .padding(18)
            .symairaCard(padding: 0)

            // Actions
            HStack(spacing: 10) {
                Button {
                    Task { await runImport(dryRun: true) }
                } label: {
                    Label("Preview (dry-run)", symairaIcon: .visible)
                }
                .symairaButtonStyle(.secondary)
                .disabled(exportDir == nil || isRunning || !hasVault)

                Button {
                    Task { await runImport(dryRun: false) }
                } label: {
                    if isRunning {
                        ProgressView()
                            .accessibilityLabel("Importing documents…")
                            .controlSize(.small)
                    } else {
                        Label("Start Import", symairaIcon: .download)
                    }
                }
                .symairaButtonStyle(.primary)
                .disabled(exportDir == nil || isRunning || !hasVault)

                Spacer()

                Button("Done") { dismiss() }
                    .symairaButtonStyle(.secondary)
                    .disabled(isRunning)
            }

            if let errorMessage {
                SymairaNotice(message: errorMessage, tone: .critical) {
                    self.errorMessage = nil
                }
            }

            if !progressMessage.isEmpty {
                Label(progressMessage, symairaIcon: .package)
                    .symairaText(.secondary)
            }

            if let summary {
                resultSummary(summary)
            }

            Spacer(minLength: 0)
        }
        .padding(28)
        .frame(width: 640, height: 560)
        .background(SymairaTokens.canvas)
    }

    private var header: some View {
        HStack(alignment: .top, spacing: 14) {
            SymairaIconView(.download, size: 26)
                .foregroundStyle(SymairaTokens.ink2)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 4) {
                Text("Import from Paperless-ngx").symairaText(.title)
                Text("Migrate your Paperless documents into the vault as contract notes with their original files archived.")
                    .symairaText(.secondary)
            }
        }
    }

    // MARK: - Result summary

    @ViewBuilder
    private func resultSummary(_ summary: DeskCore.PaperlessImportSummary) -> some View {
        let hasErrors = summary.results.contains(where: { $0.action == "error" })
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 6) {
                SymairaIconView(hasErrors ? .warning : .verified, size: 16)
                    .foregroundStyle(hasErrors ? SymairaTokens.warning : SymairaTokens.positive)
                Text(hasErrors ? "Import finished with errors" : "Import complete")
                    .symairaText(.subheading)
                Spacer()
            }

            HStack(spacing: 16) {
                summaryStat("Total", summary.total, color: SymairaTokens.ink)
                summaryStat("Created", summary.created, color: SymairaTokens.positive)
                summaryStat("Updated", summary.updated, color: SymairaTokens.ink2)
                summaryStat("Skipped", summary.skipped, color: SymairaTokens.ink2)
                if summary.errors > 0 {
                    summaryStat("Errors", summary.errors, color: SymairaTokens.critical)
                }
            }

            if !summary.results.isEmpty {
                ScrollView {
                    VStack(alignment: .leading, spacing: 2) {
                        ForEach(Array(summary.results.enumerated()), id: \.offset) { _, result in
                            resultRow(result)
                        }
                    }
                }
                .frame(maxHeight: 190)
                .padding(8)
                .background(SymairaTokens.canvas)
                .cornerRadius(8)
            }
        }
        .padding(16)
        .symairaCard(padding: 0)
    }

    private func summaryStat(_ label: String, _ value: Int, color: Color) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text("\(value)")
                .symairaText(.subheading, respectsForeground: false)
                .monospacedDigit()
                .foregroundStyle(color)
            Text(label)
                .symairaText(.secondary)
        }
    }

    @ViewBuilder
    private func resultRow(_ result: DeskCore.PaperlessImportResult) -> some View {
        HStack(spacing: 8) {
            SymairaIconView(icon(for: result.action), size: 14)
                .foregroundStyle(color(for: result.action))
            Text("#\(result.paperlessID) \(result.title)")
                .symairaText(.caption, respectsForeground: false)
                .foregroundStyle(SymairaTokens.ink)
                .lineLimit(1)
            Spacer()
            if let error = result.error {
                Text(error)
                    .symairaText(.caption, respectsForeground: false)
                    .foregroundStyle(SymairaTokens.critical)
                    .lineLimit(1)
            } else if let path = result.notePath {
                Text(path)
                    .symairaText(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
        }
        .padding(.vertical, 2)
    }

    private func icon(for action: String) -> SymairaIcon {
        switch action {
        case "created": return .add
        case "updated": return .sync
        case "skipped_idempotent": return .skipped
        case "error": return .failure
        default: return .pending
        }
    }

    private func color(for action: String) -> Color {
        switch action {
        case "created": return SymairaTokens.positive
        case "updated": return SymairaTokens.ink2
        case "skipped_idempotent": return SymairaTokens.ink2
        case "error": return SymairaTokens.critical
        default: return SymairaTokens.ink2
        }
    }

    /// A vault (local or server) must be active for the import to have a
    /// target; the onboarding screen presents the picker only after setup.
    private var hasVault: Bool {
        core.isRemote || core.vaultPath != nil
    }

    // MARK: - Actions

    private func pickDirectory() {
        isPicking = true
        let panel = NSOpenPanel()
        panel.title = "Choose Paperless Export Directory"
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.canCreateDirectories = false
        panel.allowsMultipleSelection = false
        panel.begin { response in
            isPicking = false
            guard response == .OK else { return }
            exportDir = panel.url?.path
            summary = nil
            errorMessage = nil
        }
    }

    private func runImport(dryRun: Bool) async {
        guard let exportDir else { return }
        isRunning = true
        errorMessage = nil
        summary = nil
        progressMessage = dryRun ? "Analyzing export…" : "Importing documents…"
        defer {
            isRunning = false
            progressMessage = ""
        }
        do {
            summary = try await core.paperlessImport(exportDir: exportDir, dryRun: dryRun)
        } catch {
            errorMessage = "Import failed: \(error.localizedDescription)"
        }
    }
}
