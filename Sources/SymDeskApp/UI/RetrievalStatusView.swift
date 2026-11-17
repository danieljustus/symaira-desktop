import SwiftUI
import AppKit
import SymairaTheme
import SymDeskCore

/// Health of the hybrid search index and its embedding backend (issue #515).
///
/// Retrieval ships inside SymDesk since the repo consolidation, and it
/// degrades silently by design: when the index is cold or the embedding
/// backend is unreachable, queries still answer — just worse. This screen is
/// where that becomes visible and fixable without a terminal.
struct RetrievalStatusView: View {
    @EnvironmentObject var core: DeskCore

    @State private var status: RetrievalStatus?
    @State private var loadError: String?
    @State private var isReindexing = false
    @State private var isRetrying = false
    @State private var reindexSummary: String?
    @State private var maintenanceSummary: String?
    @State private var pruneStale = true

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: SymairaSpacing.xLarge) {
                header

                if let status {
                    stateBanner(status)
                    statsCard(status)
                } else if let loadError {
                    errorCard(loadError)
                } else {
                    SymairaLoadingState("Reading the search index…")
                }

                reindexCard
            }
            .padding(SymairaSpacing.xLarge)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .navigationTitle("Search Index")
        .task { await load() }
    }

    // MARK: - Sections

    private var header: some View {
        VStack(alignment: .leading, spacing: SymairaSpacing.small) {
            Text("Search Index")
                .symairaText(.title)
            Text("Hybrid keyword and semantic search runs inside SymDesk. When the index is cold or the embedding backend is unreachable, search keeps working with worse ranking — this is where you see it and fix it.")
                .symairaText(.secondary)
        }
    }

    /// The two failure modes are deliberately distinguished: an empty index is
    /// fixed by indexing, an unreachable backend is not.
    @ViewBuilder
    private func stateBanner(_ status: RetrievalStatus) -> some View {
        if status.isEmpty {
            banner(
                tone: .warning,
                title: "The index is empty",
                detail: "Nothing is indexed yet, so search falls back to plain full-text matching. Run an index pass below."
            )
        } else if status.hasStoredDegradation {
            banner(
                tone: .warning,
                title: "The stored index needs repair",
                detail: storedDegradationDetail(status)
            )
        } else if !status.backendAvailable {
            banner(
                tone: .warning,
                title: "Embedding backend unreachable",
                detail: "The stored index is healthy, but semantic queries are temporarily unavailable. Start the embedding backend, then reload."
            )
        } else {
            banner(
                tone: .positive,
                title: "Retrieval is healthy",
                detail: "Indexed and embedding with \(status.embeddingModel)."
            )
        }
    }

    private func storedDegradationDetail(_ status: RetrievalStatus) -> String {
        var problems: [String] = []
        if let pending = status.pendingChunkCount, pending > 0 {
            problems.append("\(pending) chunk\(pending == 1 ? "" : "s") still need real embeddings")
        }
        if status.mixedEmbeddingSpaces == true {
            problems.append("stored vectors use mixed embedding spaces")
        }
        return problems.joined(separator: "; ")
            + ". Start the embedding backend, then run an index pass with re-embed to repair semantic search."
    }

    private func banner(tone: SymairaTone, title: String, detail: String) -> some View {
        SymairaNotice(title: title, message: detail, tone: tone)
    }

    private func statsCard(_ status: RetrievalStatus) -> some View {
        VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
            row("Indexed documents", "\(status.documentCount)")
            row("Chunks", "\(status.chunkCount)")
            if status.indexScope == "shared" {
                row("Index scope", "Shared across vaults")
                if let vaultCount = status.vaultDocumentCount {
                    row("Active vault files", "\(vaultCount)")
                }
            }
            row("Index size", Self.formatBytes(status.databaseBytes))
            if let location = status.indexLocation, !location.isEmpty {
                row("Index location", location)
                maintenanceControls
            }
            row("Last indexed", Self.formatTimestamp(status.lastIndexedAt))
            row("Embedding model", status.embeddingModel)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .symairaCard()
    }

    private var maintenanceControls: some View {
        VStack(alignment: .leading, spacing: SymairaSpacing.small) {
            HStack(spacing: SymairaSpacing.medium) {
                Button("Back Up…") { chooseBackupDestination() }
                    .symairaButtonStyle(.secondary)
                Button("Restore…") { chooseRestoreSource() }
                    .symairaButtonStyle(.secondary)
                Button("Relocate…") { chooseRelocationDestination() }
                    .symairaButtonStyle(.secondary)
            }
            .disabled(isReindexing || isRetrying)
            if let maintenanceSummary {
                Text(maintenanceSummary)
                    .symairaText(.secondary)
            }
        }
    }

    private func errorCard(_ message: String) -> some View {
        banner(
            tone: .critical,
            title: "The index state could not be read",
            detail: message
        )
    }

    private var reindexCard: some View {
        VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
            Text("Re-index the vault")
                .symairaText(.subheading)
            SymairaFormRow("Also remove entries for files that no longer exist") {
                Toggle(isOn: $pruneStale) { EmptyView() }
                    .toggleStyle(.symairaSwitch)
                    .accessibilityLabel("Also remove entries for files that no longer exist")
                    .disabled(isReindexing)
            }
            HStack(spacing: SymairaSpacing.medium) {
                Button(isReindexing ? "Indexing…" : "Run Index Pass") {
                    Task { await reindex() }
                }
                .symairaButtonStyle(.primary)
                .disabled(isReindexing || isRetrying || core.vaultPath == nil)

                Button(isRetrying ? "Retrying…" : "Retry failed") {
                    Task { await retryFailed() }
                }
                .symairaButtonStyle(.secondary)
                .disabled(isReindexing || isRetrying || core.vaultPath == nil)

                if isReindexing || isRetrying {
                    HStack(spacing: SymairaSpacing.small) {
                        ProgressView().controlSize(.small)
                        Text(isReindexing ? "Indexing the vault…" : "Retrying failed files…")
                            .symairaText(.secondary)
                    }
                    .accessibilityElement(children: .combine)
                }
            }
            if let reindexSummary {
                Text(reindexSummary)
                    .symairaText(.secondary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .symairaCard()
    }

    private func row(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label)
                .symairaText(.secondary)
            Spacer()
            Text(value)
                .symairaText(.monoSmall, respectsForeground: false)
                .foregroundStyle(SymairaTokens.ink)
                .textSelection(.enabled)
        }
    }

    // MARK: - Actions

    private func load() async {
        do {
            status = try await core.retrievalStatus()
            loadError = nil
        } catch {
            status = nil
            loadError = error.localizedDescription
        }
    }

    private func retryFailed() async {
        isRetrying = true
        defer { isRetrying = false }
        do {
            let result = try await core.retryFailedIndex()
            reindexSummary = "Retried \(result.attempted): \(result.succeeded) succeeded, \(result.failed) failed."
        } catch {
            reindexSummary = "Retry failed: \(error.localizedDescription)"
        }
        await load()
    }

    private func reindex() async {
        isReindexing = true
        defer { isReindexing = false }
        do {
            let result = try await core.reindexVault(prune: pruneStale)
            var summary = "Indexed \(result.indexed), skipped \(result.skipped)"
            if let pruned = result.pruned {
                summary += ", pruned \(pruned)"
            }
            reindexSummary = summary + "."
        } catch {
            reindexSummary = "Index pass failed: \(error.localizedDescription)"
        }
        await load()
    }

    private func chooseBackupDestination() {
        let panel = NSSavePanel()
        panel.title = "Back Up Search Index"
        panel.nameFieldStringValue = "symseek.db.backup"
        panel.begin { response in
            guard response == .OK, let url = panel.url else { return }
            Task { @MainActor in
                do {
                    try await core.backupIndex(to: url.path)
                    maintenanceSummary = "Backup created at \(url.path)."
                } catch {
                    maintenanceSummary = "Backup failed: \(error.localizedDescription)"
                }
            }
        }
    }

    private func chooseRestoreSource() {
        let panel = NSOpenPanel()
        panel.title = "Restore Search Index"
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false
        panel.begin { response in
            guard response == .OK, let url = panel.url else { return }
            Task { @MainActor in
                do {
                    try await core.restoreIndex(from: url.path)
                    maintenanceSummary = "Index restored from \(url.path)."
                    await load()
                } catch {
                    maintenanceSummary = "Restore failed: \(error.localizedDescription)"
                }
            }
        }
    }

    private func chooseRelocationDestination() {
        let panel = NSSavePanel()
        panel.title = "Relocate Search Index"
        panel.nameFieldStringValue = "symseek.db"
        panel.begin { response in
            guard response == .OK, let url = panel.url else { return }
            Task { @MainActor in
                do {
                    try await core.relocateIndex(to: url.path)
                    maintenanceSummary = "Index relocated to \(url.path)."
                    await load()
                } catch {
                    maintenanceSummary = "Relocation failed: \(error.localizedDescription)"
                }
            }
        }
    }

    // MARK: - Formatting

    static func formatBytes(_ bytes: Int64) -> String {
        let formatter = ByteCountFormatter()
        formatter.allowedUnits = [.useMB, .useGB]
        formatter.countStyle = .file
        return formatter.string(fromByteCount: bytes)
    }

    /// The core omits the timestamp when the index carries no such metadata;
    /// "unknown" is the honest rendering, not an epoch date.
    static func formatTimestamp(_ raw: String?) -> String {
        guard let raw, !raw.isEmpty else { return "unknown" }
        let parser = ISO8601DateFormatter()
        guard let date = parser.date(from: raw) else { return raw }
        let display = DateFormatter()
        display.dateStyle = .medium
        display.timeStyle = .short
        return display.string(from: date)
    }
}
