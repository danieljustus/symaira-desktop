import SwiftUI
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
    @State private var reindexSummary: String?
    @State private var pruneStale = true

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                header

                if let status {
                    stateBanner(status)
                    statsCard(status)
                } else if let loadError {
                    errorCard(loadError)
                } else {
                    ProgressView("Reading the search index…")
                        .tint(SymairaTheme.goldPrimary)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 20)
                }

                reindexCard
            }
            .padding(28)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .navigationTitle("Search Index")
        .task { await load() }
    }

    // MARK: - Sections

    private var header: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Search Index")
                .symairaText(.title).bold()
                .foregroundColor(SymairaTheme.textPrimary)
            Text("Hybrid keyword and semantic search runs inside SymDesk. When the index is cold or the embedding backend is unreachable, search keeps working with worse ranking — this is where you see it and fix it.")
                .symairaText(.callout)
                .foregroundColor(SymairaTheme.textSecondary)
        }
    }

    /// The two failure modes are deliberately distinguished: an empty index is
    /// fixed by indexing, an unreachable backend is not.
    @ViewBuilder
    private func stateBanner(_ status: RetrievalStatus) -> some View {
        if status.isEmpty {
            banner(
                icon: "tray",
                tint: .orange,
                title: "The index is empty",
                detail: "Nothing is indexed yet, so search falls back to plain full-text matching. Run an index pass below."
            )
        } else if status.hasStoredDegradation {
            banner(
                icon: "exclamationmark.triangle.fill",
                tint: .orange,
                title: "The stored index needs repair",
                detail: storedDegradationDetail(status)
            )
        } else if !status.backendAvailable {
            banner(
                icon: "bolt.horizontal.circle",
                tint: .orange,
                title: "Embedding backend unreachable",
                detail: "The stored index is healthy, but semantic queries are temporarily unavailable. Start the embedding backend, then reload."
            )
        } else {
            banner(
                icon: "checkmark.seal.fill",
                tint: .green,
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

    private func banner(icon: String, tint: Color, title: String, detail: String) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: icon)
                .symairaText(.heading)
                .foregroundColor(tint)
            VStack(alignment: .leading, spacing: 4) {
                Text(title)
                    .symairaText(.body).fontWeight(.semibold)
                    .foregroundColor(SymairaTheme.textPrimary)
                Text(detail)
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textSecondary)
            }
            Spacer()
        }
        .padding(14)
        .glassCard()
    }

    private func statsCard(_ status: RetrievalStatus) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            row("Documents", "\(status.documentCount)")
            row("Chunks", "\(status.chunkCount)")
            row("Index size", Self.formatBytes(status.databaseBytes))
            row("Last indexed", Self.formatTimestamp(status.lastIndexedAt))
            row("Embedding model", status.embeddingModel)
        }
        .padding(14)
        .glassCard()
    }

    private func errorCard(_ message: String) -> some View {
        banner(
            icon: "exclamationmark.triangle.fill",
            tint: .red,
            title: "The index state could not be read",
            detail: message
        )
    }

    private var reindexCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Re-index the vault")
                .symairaText(.subheading)
                .foregroundColor(SymairaTheme.goldPrimary)
            Toggle("Also remove entries for files that no longer exist", isOn: $pruneStale)
                .symairaText(.caption)
                .disabled(isReindexing)
            HStack(spacing: 12) {
                Button(isReindexing ? "Indexing…" : "Run Index Pass") {
                    Task { await reindex() }
                }
                .buttonStyle(.borderedProminent)
                .tint(SymairaTheme.goldPrimary)
                .disabled(isReindexing || core.vaultPath == nil)

                if isReindexing {
                    ProgressView().controlSize(.small)
                }
            }
            if let reindexSummary {
                Text(reindexSummary)
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textSecondary)
            }
        }
        .padding(14)
        .glassCard()
    }

    private func row(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label)
                .symairaText(.caption)
                .foregroundColor(SymairaTheme.textSecondary)
            Spacer()
            Text(value)
                .symairaText(.caption).monospaced()
                .foregroundColor(SymairaTheme.textPrimary)
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
