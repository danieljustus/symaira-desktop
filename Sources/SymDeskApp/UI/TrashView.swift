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
                Image(systemName: "trash")
                    .foregroundColor(SymairaTheme.goldPrimary)
                Text("Trash")
                    .symairaText(.title).fontWeight(.semibold)
                    .foregroundColor(SymairaTheme.textPrimary)
                Spacer()
                if !trashItems.isEmpty {
                    Button(role: .destructive) {
                        showPurgeAllConfirmation = true
                    } label: {
                        Label("Purge All", systemImage: "trash.slash")
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                    .tint(.red)
                }
                Button(action: { Task { await loadTrash() } }) {
                    Label("Refresh", systemImage: "arrow.clockwise")
                }
                .buttonStyle(.plain)
                .foregroundColor(SymairaTheme.textSecondary)
                .disabled(isLoading)
            }
            .padding(.horizontal, 20)
            .padding(.top, 16)
            .padding(.bottom, 12)

            Text("Items moved to the trash can be restored to their original location or permanently deleted.")
                .symairaText(.callout)
                .foregroundColor(SymairaTheme.textSecondary)
                .padding(.horizontal, 20)
                .padding(.bottom, 12)

            if let error = errorMessage {
                HStack(spacing: 8) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red)
                    Text(error)
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textSecondary)
                    Spacer()
                    Button("Retry") {
                        Task { await loadTrash() }
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                }
                .padding(10)
                .background(Color.red.opacity(0.12))
                .cornerRadius(6)
                .padding(.horizontal, 20)
                .padding(.bottom, 8)
            }

            if isLoading {
                Spacer()
                ProgressView("Loading trash…")
                    .tint(SymairaTheme.goldPrimary)
                    .foregroundColor(SymairaTheme.textSecondary)
                Spacer()
            } else if trashItems.isEmpty {
                Spacer()
                ContentUnavailableView {
                    Label("Trash is Empty", systemImage: "trash.slash")
                } description: {
                    Text("Deleted notes and documents will appear here so you can restore them.")
                }
                .foregroundColor(SymairaTheme.textMuted)
                Spacer()
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
        .background(SymairaTheme.bgDarker)
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
            Image(systemName: "doc.text")
                .symairaText(.heading)
                .foregroundColor(SymairaTheme.textMuted)

            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(displayName(from: item.originalPath))
                        .symairaText(.body).fontWeight(.medium)
                        .foregroundColor(SymairaTheme.textPrimary)
                        .lineLimit(1)
                }

                HStack(spacing: 8) {
                    Label(item.originalPath, systemImage: "folder")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                        .lineLimit(1)

                    Text("·")
                        .foregroundColor(SymairaTheme.textMuted)

                    Label(formattedDate(item.deletedAt), systemImage: "clock")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)

                    Text("·")
                        .foregroundColor(SymairaTheme.textMuted)

                    Text(formattedSize(item.size))
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                }
            }

            Spacer()

            HStack(spacing: 8) {
                Button("Restore") {
                    Task { await restoreItem(item) }
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
                .tint(SymairaTheme.goldPrimary)
                .disabled(isBusy)

                Button(role: .destructive) {
                    Task { await purgeItem(item) }
                } label: {
                    Label("Delete", systemImage: "xmark")
                        .labelStyle(.iconOnly)
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .tint(.red)
                .disabled(isBusy)
            }
        }
        .padding(12)
        .background(SymairaTheme.bgDark.opacity(0.3))
        .cornerRadius(8)
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(SymairaTheme.borderGlassHover, lineWidth: 0.5)
        )
        .opacity(isBusy ? 0.6 : 1)
        .overlay {
            if isBusy {
                ProgressView()
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
