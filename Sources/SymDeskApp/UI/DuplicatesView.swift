import SwiftUI
import SymairaTheme
import SymDeskCore

/// "Possible duplicates" lane: every cluster the vault-wide SimHash scan
/// found, with per-member similarity and merge/dismiss actions (issue #307).
///
/// - Merge: opens the pair in the document grid for manual resolution (the
///   app never deletes files automatically); a "Move to Trash" shortcut is
///   offered per member so the user can clean up after merging content.
/// - Dismiss: hides the group for this session; a Refresh re-runs the scan.
struct DuplicatesView: View {
    @EnvironmentObject var core: DeskCore

    @State private var groups: [DeskCore.DuplicateGroup] = []
    @State private var isLoading = false
    @State private var errorMessage: String? = nil
    @State private var dismissedPaths = Set<String>()
    @State private var pendingTrash: DeskCore.DuplicateGroup.Member?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Header
            HStack {
                Image(systemName: "arrow.triangle.2.circlepath")
                    .foregroundColor(SymairaTheme.goldPrimary)
                Text("Possible Duplicates")
                    .font(.title2.weight(.semibold))
                    .foregroundColor(SymairaTheme.textPrimary)
                Spacer()
                Button(action: { Task { await loadDuplicates() } }) {
                    Label("Refresh", systemImage: "arrow.clockwise")
                }
                .buttonStyle(.plain)
                .foregroundColor(SymairaTheme.textSecondary)
                .disabled(isLoading)
            }
            .padding(.horizontal, 20)
            .padding(.top, 16)
            .padding(.bottom, 12)

            Text("Documents whose content looks near-identical, detected by SimHash while indexing. Merge the content you want to keep, then trash the rest.")
                .font(.callout)
                .foregroundColor(SymairaTheme.textSecondary)
                .padding(.horizontal, 20)
                .padding(.bottom, 12)

            if let error = errorMessage {
                HStack(spacing: 8) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red)
                    Text(error)
                        .font(.caption)
                        .foregroundColor(SymairaTheme.textSecondary)
                    Spacer()
                    Button("Retry") { Task { await loadDuplicates() } }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                }
                .padding(10)
                .background(Color.red.opacity(0.12))
                .cornerRadius(6)
                .padding(.horizontal, 20)
                .padding(.bottom, 8)
            }

            if isLoading && groups.isEmpty {
                Spacer()
                ProgressView("Scanning for duplicates…")
                    .tint(SymairaTheme.goldPrimary)
                    .foregroundColor(SymairaTheme.textSecondary)
                Spacer()
            } else if visibleGroups.isEmpty {
                Spacer()
                ContentUnavailableView {
                    Label("No Duplicates Found", systemImage: "checkmark.seal")
                } description: {
                    Text(isLoading ? "Scanning…" : "No near-identical documents detected in this vault.")
                }
                .foregroundColor(SymairaTheme.textMuted)
                Spacer()
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 12) {
                        ForEach(visibleGroups) { group in
                            groupCard(group)
                        }
                    }
                    .padding(16)
                }
            }
        }
        .background(SymairaTheme.bgDark)
        .task { await loadDuplicates() }
        .confirmationDialog(
            "Move “\(pendingTrash?.title ?? "")” to Trash?",
            isPresented: Binding(
                get: { pendingTrash != nil },
                set: { if !$0 { pendingTrash = nil } }
            ),
            titleVisibility: .visible
        ) {
            Button("Move to Trash", role: .destructive) {
                if let member = pendingTrash {
                    Task { await trash(member) }
                }
                pendingTrash = nil
            }
            Button("Cancel", role: .cancel) { pendingTrash = nil }
        } message: {
            Text("Merge the content you want to keep before trashing. The file stays restorable from the Trash screen.")
        }
    }

    private var visibleGroups: [DeskCore.DuplicateGroup] {
        groups.filter { group in
            !dismissedPaths.contains(group.path)
                && !group.members.contains { dismissedPaths.contains($0.path) }
        }
    }

    // MARK: - Group card

    private func groupCard(_ group: DeskCore.DuplicateGroup) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                Image(systemName: "doc.on.doc")
                    .foregroundColor(SymairaTheme.goldSecondary)
                Text(group.title.isEmpty ? group.path : group.title)
                    .font(.headline)
                    .foregroundColor(SymairaTheme.textPrimary)
                    .lineLimit(1)
                Text("+\(group.members.count) more")
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textSecondary)
                Spacer()
                Button {
                    dismissGroup(group)
                } label: {
                    Label("Dismiss", systemImage: "xmark.circle")
                }
                .buttonStyle(.plain)
                .foregroundColor(SymairaTheme.textSecondary)
                .help("Hide this group for now")
            }

            // Representative
            memberRow(path: group.path, title: group.title, similarity: nil, isRepresentative: true)

            // Similar members
            ForEach(group.members, id: \.path) { member in
                memberRow(path: member.path, title: member.title, similarity: member.similarity, isRepresentative: false)
            }
        }
        .padding(12)
        .background(SymairaTheme.bgDark.opacity(0.3))
        .cornerRadius(8)
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(SymairaTheme.borderGlassHover, lineWidth: 0.5)
        )
    }

    private func memberRow(path: String, title: String, similarity: Int?, isRepresentative: Bool) -> some View {
        HStack(spacing: 8) {
            Image(systemName: isRepresentative ? "star.fill" : "doc")
                .font(.caption)
                .foregroundColor(isRepresentative ? SymairaTheme.goldPrimary : SymairaTheme.textMuted)
            VStack(alignment: .leading, spacing: 1) {
                Text(title.isEmpty ? path : title)
                    .font(.callout)
                    .foregroundColor(SymairaTheme.textPrimary)
                    .lineLimit(1)
                Text(path)
                    .font(.caption2)
                    .foregroundColor(SymairaTheme.textMuted)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer()
            if let similarity {
                Text("\(similarity)% similar")
                    .font(.caption2)
                    .foregroundColor(SymairaTheme.textSecondary)
            } else {
                Text("Reference")
                    .font(.caption2)
                    .foregroundColor(SymairaTheme.goldSecondary)
            }
            Button {
                pendingTrash = DeskCore.DuplicateGroup.Member(path: path, title: title, similarity: similarity ?? 0)
            } label: {
                Image(systemName: "trash")
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
            }
            .buttonStyle(.plain)
            .help("Move to Trash after merging")
        }
        .padding(.vertical, 2)
    }

    // MARK: - Actions

    private func loadDuplicates() async {
        isLoading = true
        errorMessage = nil
        defer { isLoading = false }
        do {
            groups = try await core.duplicates()
        } catch {
            errorMessage = "Failed to scan duplicates: \(error.localizedDescription)"
            groups = []
        }
    }

    private func dismissGroup(_ group: DeskCore.DuplicateGroup) {
        dismissedPaths.insert(group.path)
        for member in group.members {
            dismissedPaths.insert(member.path)
        }
    }

    private func trash(_ member: DeskCore.DuplicateGroup.Member) async {
        do {
            try await core.noteDelete(path: member.path)
            // Refresh so the trashed member disappears from the lane.
            await loadDuplicates()
        } catch {
            errorMessage = "Could not move to trash: \(error.localizedDescription)"
        }
    }
}
