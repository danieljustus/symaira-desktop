import SwiftUI
import SymairaTheme
import SymDeskCore

// MARK: - TrashView

struct TrashView: View {
    @EnvironmentObject var core: DeskCore

    @State private var trashItems: [TrashEntry] = []
    @State private var isLoading = false
    @State private var errorMessage: String? = nil
    @State private var showPurgeConfirmation = false
    @State private var showPurgeAllConfirmation = false
    @State private var actionInProgress = Set<String>()

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Header
            HStack {
                SymairaIconView(.delete, size: 16)
                    .foregroundStyle(SymairaTokens.ink2)
                Text("Trash")
                    .symairaText(.title)
                Spacer()
                if !trashItems.isEmpty {
                    Button(role: .destructive) {
                        showPurgeAllConfirmation = true
                    } label: {
                        Label("Purge All", symairaIcon: .delete)
                    }
                    .symairaButtonStyle(.secondary)
                }
                Button(action: { Task { await loadTrash() } }) {
                    Label("Refresh", symairaIcon: .refresh)
                }
                .symairaButtonStyle(.secondary)
                .disabled(isLoading)
            }
            .padding(.horizontal, 20)
            .padding(.top, 16)
            .padding(.bottom, 12)

            Text("Items moved to the trash can be restored to their original location or permanently deleted.")
                .symairaText(.secondary)
                .padding(.horizontal, 20)
                .padding(.bottom, 12)

            if let error = errorMessage {
                HStack(spacing: 8) {
                    SymairaNotice(message: error, tone: .critical)
                    Button("Retry") {
                        Task { await loadTrash() }
                    }
                    .symairaButtonStyle(.secondary)
                }
                .padding(.horizontal, 20)
                .padding(.bottom, 8)
            }

            if isLoading {
                SymairaLoadingState("Loading trash…")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if trashItems.isEmpty {
                SymairaEmptyState(
                    icon: .delete,
                    title: "Trash Is Empty",
                    message: "Deleted notes and documents will appear here so you can restore them."
                )
            } else {
                ScrollView {
                    VStack(spacing: 6) {
                        ForEach(trashItems) { item in
                            trashItemRow(item)
                        }
                    }
                    .padding(16)
                }
            }
        }
        .background(SymairaTokens.canvas)
        .navigationTitle("Trash")
        .task { await loadTrash() }
        .alert("Confirm Purge", isPresented: $showPurgeAllConfirmation) {
            Button("Cancel", role: .cancel) {}
            Button("Purge All", role: .destructive) {
                Task { await purgeAll() }
            }
        } message: {
            Text("This will permanently delete all \(trashItems.count) items from the trash. This action cannot be undone.")
        }
    }

    // MARK: - Trash Item Row

    private func trashItemRow(_ item: TrashEntry) -> some View {
        let isBusy = actionInProgress.contains(item.name)

        return HStack(spacing: 12) {
            SymairaIconView(.document, size: 20)
                .foregroundStyle(SymairaTokens.ink3)

            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(displayName(from: item.originalPath))
                        .symairaText(.bodyEmphasized)
                        .lineLimit(1)
                }

                HStack(spacing: 8) {
                    Label(item.originalPath, symairaIcon: .folder)
                        .symairaText(.caption)
                        .lineLimit(1)

                    Text("·")
                        .symairaText(.caption)

                    Label(formattedDate(item.deletedAt), symairaIcon: .time)
                        .symairaText(.caption)

                    Text("·")
                        .symairaText(.caption)

                    Text(formattedSize(item.size))
                        .symairaText(.caption)
                        .monospacedDigit()
                }
            }

            Spacer()

            HStack(spacing: 8) {
                Button("Restore") {
                    Task { await restoreItem(item) }
                }
                .symairaButtonStyle(.secondary)
                .disabled(isBusy)

                Button(role: .destructive) {
                    Task { await purgeItem(item) }
                } label: {
                    Label("Delete", symairaIcon: .delete)
                        .labelStyle(.iconOnly)
                        .accessibilityLabel("Delete")
                }
                .symairaButtonStyle(.secondary)
                .disabled(isBusy)
            }
        }
        .symairaCard(padding: SymairaSpacing.medium)
        .opacity(isBusy ? 0.6 : 1)
        .overlay {
            if isBusy {
                ProgressView()
                    .accessibilityLabel("Loading trash…")
                    .controlSize(.small)
            }
        }
    }

    // MARK: - Actions

    private func loadTrash() async {
        isLoading = true
        errorMessage = nil
        defer { isLoading = false }
        do {
            trashItems = try await core.trashList()
        } catch {
            errorMessage = "Failed to load trash: \(error.localizedDescription)"
        }
    }

    private func restoreItem(_ item: TrashEntry) async {
        actionInProgress.insert(item.name)
        defer { actionInProgress.remove(item.name) }
        do {
            try await core.trashRestore(name: item.name)
            // Remove from local list on success
            trashItems.removeAll { $0.name == item.name }
        } catch {
            errorMessage = "Failed to restore \(displayName(from: item.originalPath)): \(error.localizedDescription)"
        }
    }

    private func purgeItem(_ item: TrashEntry) async {
        actionInProgress.insert(item.name)
        defer { actionInProgress.remove(item.name) }
        do {
            // Purge single item by purging all and re-listing. We use a
            // targeted approach: the CLI only supports bulk purge, so we
            // temporarily track purged names via filtering.
            // A better approach: we note the item name, purge all old items,
            // then reload. But since purge all is destructive, we need a
            // smarter approach.
            //
            // Workaround: call purge with --all to clear everything, then
            // re-list. For single-item purge, we could just note that the
            // current CLI doesn't support per-item purge. The purge all is
            // available via bulk action.
            errorMessage = "Individual purge is not supported — use Purge All to clear all trash items."
        }
    }

    private func purgeAll() async {
        isLoading = true
        errorMessage = nil
        defer { isLoading = false }
        do {
            try await core.trashPurgeAll()
            trashItems = []
        } catch {
            errorMessage = "Failed to purge trash: \(error.localizedDescription)"
        }
    }

    // MARK: - Formatting

    private func displayName(from originalPath: String) -> String {
        let url = URL(fileURLWithPath: originalPath)
        let filename = url.lastPathComponent
        return (filename as NSString).deletingPathExtension
    }

    private func formattedDate(_ raw: String) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = formatter.date(from: raw) {
            let display = DateFormatter()
            display.dateStyle = .medium
            display.timeStyle = .short
            return "Deleted \(display.string(from: date))"
        }
        formatter.formatOptions = [.withInternetDateTime]
        if let date = formatter.date(from: raw) {
            let display = DateFormatter()
            display.dateStyle = .medium
            display.timeStyle = .short
            return "Deleted \(display.string(from: date))"
        }
        return "Deleted \(raw)"
    }

    private func formattedSize(_ size: Int64) -> String {
        let formatter = ByteCountFormatter()
        formatter.countStyle = .file
        return formatter.string(fromByteCount: size)
    }
}
