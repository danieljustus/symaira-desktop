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
                            .symairaText(.callout)
                            .foregroundStyle(exportDir == nil ? SymairaTheme.textSecondary : SymairaTheme.textPrimary)
                            .lineLimit(1)
                            .truncationMode(.middle)
                            .frame(width: 300, alignment: .leading)
                        Button("Choose…") { pickDirectory() }
                            .buttonStyle(.bordered)
                            .controlSize(.small)
                    }
                }
                Text("A Paperless-ngx export (manifest.json + document files). The import is idempotent — re-running it updates notes instead of duplicating them.")
                    .symairaText(.caption)
                    .foregroundStyle(SymairaTheme.textSecondary)
            }
            .padding(18)
            .glassmorphicPanel()

            // Actions
            HStack(spacing: 10) {
                Button {
                    Task { await runImport(dryRun: true) }
                } label: {
                    Label("Preview (dry-run)", systemImage: "eye")
                }
                .buttonStyle(.bordered)
                .disabled(exportDir == nil || isRunning || !hasVault)

                Button {
                    Task { await runImport(dryRun: false) }
                } label: {
                    if isRunning {
                        ProgressView()
                            .controlSize(.small)
                    } else {
                        Label("Start Import", systemImage: "arrow.down.doc")
                    }
                }
                .buttonStyle(.borderedProminent)
                .tint(SymairaTheme.goldPrimary)
                .disabled(exportDir == nil || isRunning || !hasVault)

                Spacer()

                Button("Done") { dismiss() }
                    .buttonStyle(.bordered)
                    .disabled(isRunning)
            }

            if let errorMessage {
                HStack(spacing: 8) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red)
                    Text(errorMessage)
                        .symairaText(.caption)
                        .foregroundStyle(SymairaTheme.textSecondary)
                    Spacer()
                }
                .padding(10)
                .background(Color.red.opacity(0.12))
                .cornerRadius(6)
            }

            if !progressMessage.isEmpty {
                Label(progressMessage, systemImage: "shippingbox")
                    .symairaText(.callout)
                    .foregroundStyle(SymairaTheme.textSecondary)
            }

            if let summary {
                resultSummary(summary)
            }

            Spacer(minLength: 0)
        }
        .padding(28)
        .frame(width: 640, height: 560)
        .background(SymairaTheme.bgDark)
    }

    private var header: some View {
        HStack(alignment: .top, spacing: 14) {
            Image(systemName: "doc.badge.arrow.up")
                .font(.system(size: 28))
                .foregroundStyle(SymairaTheme.goldPrimary)
            VStack(alignment: .leading, spacing: 4) {
                Text("Import from Paperless-ngx").symairaText(.title).bold()
                Text("Migrate your Paperless documents into the vault as contract notes with their original files archived.")
                    .foregroundStyle(SymairaTheme.textSecondary)
            }
        }
    }

    // MARK: - Result summary

    @ViewBuilder
    private func resultSummary(_ summary: DeskCore.PaperlessImportSummary) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 6) {
                Image(systemName: "checkmark.seal")
                    .foregroundStyle(SymairaTheme.goldPrimary)
                Text(summary.results.contains(where: { $0.action == "error" }) ? "Import finished with errors" : "Import complete")
                    .symairaText(.subheading)
                    .foregroundStyle(SymairaTheme.textPrimary)
                Spacer()
            }

            HStack(spacing: 16) {
                summaryStat("Total", summary.total, color: SymairaTheme.textPrimary)
                summaryStat("Created", summary.created, color: .green)
                summaryStat("Updated", summary.updated, color: SymairaTheme.goldSecondary)
                summaryStat("Skipped", summary.skipped, color: SymairaTheme.textSecondary)
                if summary.errors > 0 {
                    summaryStat("Errors", summary.errors, color: .red)
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
                .background(SymairaTheme.bgDark.opacity(0.4))
                .cornerRadius(8)
            }
        }
        .padding(16)
        .glassmorphicPanel()
    }

    private func summaryStat(_ label: String, _ value: Int, color: Color) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text("\(value)")
                .symairaText(.subheading).fontWeight(.bold)
                .foregroundStyle(color)
            Text(label)
                .symairaText(.caption)
                .foregroundStyle(SymairaTheme.textSecondary)
        }
    }

    @ViewBuilder
    private func resultRow(_ result: DeskCore.PaperlessImportResult) -> some View {
        HStack(spacing: 8) {
            Image(systemName: icon(for: result.action))
                .symairaText(.caption)
                .foregroundStyle(color(for: result.action))
            Text("#\(result.paperlessID) \(result.title)")
                .symairaText(.caption)
                .foregroundStyle(SymairaTheme.textPrimary)
                .lineLimit(1)
            Spacer()
            if let error = result.error {
                Text(error)
                    .symairaText(.caption)
                    .foregroundStyle(.red)
                    .lineLimit(1)
            } else if let path = result.notePath {
                Text(path)
                    .symairaText(.caption)
                    .foregroundStyle(SymairaTheme.textSecondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
        }
        .padding(.vertical, 2)
    }

    private func icon(for action: String) -> String {
        switch action {
        case "created": return "plus.circle"
        case "updated": return "arrow.triangle.2.circlepath"
        case "skipped_idempotent": return "equal.circle"
        case "error": return "xmark.octagon"
        default: return "circle"
        }
    }

    private func color(for action: String) -> Color {
        switch action {
        case "created": return .green
        case "updated": return SymairaTheme.goldSecondary
        case "skipped_idempotent": return SymairaTheme.textSecondary
        case "error": return .red
        default: return SymairaTheme.textSecondary
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
