import SwiftUI
import UniformTypeIdentifiers

struct MobileRootView: View {
    @EnvironmentObject private var vault: MobileVaultStore

    var body: some View {
        Group {
			if !vault.isConfigured {
                MobileOnboardingView()
            } else {
                MobileWorkspaceView()
            }
        }
        .background(MobileTheme.background)
        .task {
			if vault.isConfigured, vault.notes.isEmpty {
                await vault.reload()
            }
        }
        .alert("Vault unavailable", isPresented: errorIsPresented) {
            Button("OK", role: .cancel) { vault.errorMessage = nil }
        } message: {
            Text(vault.errorMessage ?? "Please try again.")
        }
    }

    private var errorIsPresented: Binding<Bool> {
        Binding(
            get: { vault.errorMessage != nil },
            set: { if !$0 { vault.errorMessage = nil } }
        )
    }
}

private struct MobileOnboardingView: View {
    @EnvironmentObject private var vault: MobileVaultStore
    @State private var isImporterPresented = false
	@State private var isServerSheetPresented = false

    var body: some View {
        MobileBackdrop {
            ScrollView {
                VStack(alignment: .leading, spacing: 28) {
                    Spacer(minLength: 34)

                    VStack(alignment: .leading, spacing: 12) {
                        Image(systemName: "square.grid.2x2.fill")
                            .font(.system(size: 34, weight: .medium))
                            .foregroundStyle(MobileTheme.gold)
                            .accessibilityHidden(true)

                        Text("SymDesk")
                            .font(.system(size: 44, weight: .bold, design: .rounded))
                            .foregroundStyle(MobileTheme.textPrimary)

                        Text("Your vault. Wherever you are.")
                            .font(.title2.weight(.medium))
                            .foregroundStyle(MobileTheme.goldSoft)

						Text("Open a local Markdown vault from Files or connect to your self-hosted SymDesk Server.")
                            .font(.body)
                            .foregroundStyle(MobileTheme.textSecondary)
                            .lineSpacing(4)
                    }

                    VStack(spacing: 0) {
                        OnboardingFeature(
                            icon: "icloud",
                            title: "Native iCloud access",
                            detail: "The Files app handles syncing. There is no proprietary cloud database."
                        )
                        Divider().overlay(MobileTheme.border).padding(.leading, 52)
                        OnboardingFeature(
                            icon: "bolt.horizontal.circle",
                            title: "Fast and offline-ready",
                            detail: "Changed notes are re-read; everything else comes from the local cache."
                        )
                        Divider().overlay(MobileTheme.border).padding(.leading, 52)
                        OnboardingFeature(
                            icon: "doc.richtext",
                            title: "Real document previews",
                            detail: "PDFs, images and Office files open with the native Quick Look viewer."
                        )
                    }
                    .padding(.horizontal, 16)
                    .mobileLiquidGlass(elevated: true)

                    Button {
                        isImporterPresented = true
                    } label: {
                        Label("Choose vault in Files", systemImage: "folder.badge.plus")
                            .font(.headline)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 7)
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .tint(MobileTheme.gold)
                    .foregroundStyle(.black)

					Button {
						isServerSheetPresented = true
					} label: {
						Label("Connect to SymDesk Server", systemImage: "server.rack")
							.font(.headline)
							.frame(maxWidth: .infinity)
							.padding(.vertical, 7)
					}
					.buttonStyle(.bordered)
					.controlSize(.large)
					.tint(MobileTheme.gold)

                    Text("Choose the vault folder itself, not an individual note. SymDesk never uploads your files.")
                        .font(.footnote)
                        .foregroundStyle(MobileTheme.textMuted)
                        .frame(maxWidth: .infinity)
                        .multilineTextAlignment(.center)

                    Spacer(minLength: 24)
                }
                .padding(.horizontal, 22)
                .frame(maxWidth: 620)
                .frame(maxWidth: .infinity)
            }
        }
        .fileImporter(
            isPresented: $isImporterPresented,
            allowedContentTypes: [.folder],
            allowsMultipleSelection: false
        ) { result in
            switch result {
            case .success(let urls):
                if let url = urls.first { vault.selectVault(url) }
            case .failure(let error):
                vault.errorMessage = error.localizedDescription
            }
        }
		.sheet(isPresented: $isServerSheetPresented) {
			MobileServerConnectionView {
				isServerSheetPresented = false
			}
		}
    }
}

private struct OnboardingFeature: View {
    let icon: String
    let title: String
    let detail: String

    var body: some View {
        HStack(alignment: .top, spacing: 14) {
            Image(systemName: icon)
                .font(.title3)
                .foregroundStyle(MobileTheme.gold)
                .frame(width: 38, height: 38)
                .background(MobileTheme.gold.opacity(0.1), in: RoundedRectangle(cornerRadius: 12))
                .accessibilityHidden(true)

            VStack(alignment: .leading, spacing: 3) {
                Text(title)
                    .font(.headline)
                    .foregroundStyle(MobileTheme.textPrimary)
                Text(detail)
                    .font(.subheadline)
                    .foregroundStyle(MobileTheme.textSecondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 0)
        }
        .padding(.vertical, 14)
    }
}

private struct MobileWorkspaceView: View {
    @EnvironmentObject private var vault: MobileVaultStore
    @State private var isComposerPresented = false
    @State private var composerNote: MobileNote?
    @State private var resumeDraft: MobileDraftStore.Draft?
    @State private var isScannerPresented = false

    var body: some View {
        TabView {
            MobileOverviewView(isComposerPresented: $isComposerPresented, resumeDraft: $resumeDraft, isScannerPresented: $isScannerPresented)
                .tabItem { Label("Overview", systemImage: "sparkles.rectangle.stack") }

            MobileLibraryView(documentsOnly: false, isComposerPresented: $isComposerPresented, composerNote: $composerNote, isScannerPresented: $isScannerPresented)
                .tabItem { Label("Notes", systemImage: "note.text") }

            MobileLibraryView(documentsOnly: true, isComposerPresented: $isComposerPresented, composerNote: $composerNote, isScannerPresented: $isScannerPresented)
                .tabItem { Label("Documents", systemImage: "doc.text.image") }

            MobileSettingsView()
                .tabItem { Label("Settings", systemImage: "gearshape") }
        }
        .toolbarBackground(.visible, for: .tabBar)
        .toolbarBackground(.ultraThinMaterial, for: .tabBar)
        .sheet(isPresented: $isComposerPresented) {
            MobileComposerView(editingNote: composerNote, resumeDraft: resumeDraft)
                .environmentObject(vault)
        }
        .sheet(isPresented: $isScannerPresented) {
            MobileScanView()
                .environmentObject(vault)
        }
    }
}

private struct MobileOverviewView: View {
    @EnvironmentObject private var vault: MobileVaultStore
    @Binding var isComposerPresented: Bool
    @Binding var resumeDraft: MobileDraftStore.Draft?
    @Binding var isScannerPresented: Bool
    @State private var recoveredDrafts: [MobileDraftStore.Draft] = []
    private let draftStore = try? MobileDraftStore()

    private var recentNotes: [MobileNote] {
        Array(vault.notes.prefix(5))
    }

    private var upcomingDocuments: [MobileNote] {
        Array(
            vault.documents
                .filter { !$0.dueDate.isEmpty && $0.status != "done" && $0.status != "paid" }
                .sorted { $0.dueDate < $1.dueDate }
                .prefix(4)
        )
    }

    var body: some View {
        NavigationStack {
            MobileBackdrop {
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 22) {
                        overviewHeader
                        metrics

                        if vault.pendingWriteCount > 0 {
                            MobileOutboxBanner()
                        }

                        if !recoveredDrafts.isEmpty {
                            draftRecovery
                        }

                        if !vault.recentlyOpened.isEmpty {
                            section(title: "Recently opened", systemImage: "clock.fill", notes: vault.recentlyOpened)
                        }

                        if !upcomingDocuments.isEmpty {
                            section(title: "Due next", systemImage: "calendar.badge.clock", notes: upcomingDocuments)
                        }

                        section(title: "Recently changed", systemImage: "clock.arrow.circlepath", notes: recentNotes)

                        if vault.skippedFiles > 0 {
                            Label(
                                "\(vault.skippedFiles) note\(vault.skippedFiles == 1 ? "" : "s") could not be read. Pull to refresh after iCloud finishes downloading.",
                                systemImage: "icloud.and.arrow.down"
                            )
                            .font(.footnote)
                            .foregroundStyle(MobileTheme.textSecondary)
                            .padding(16)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .mobileLiquidGlass(cornerRadius: 16)
                        }
                    }
                    .padding(.horizontal, 16)
                    .padding(.top, 18)
                    .padding(.bottom, 36)
                    .frame(maxWidth: 760)
                    .frame(maxWidth: .infinity)
                }
                .refreshable { await vault.reload() }
                .overlay {
                    if vault.isLoading && vault.notes.isEmpty {
                        ProgressView("Reading vault…")
                            .padding(20)
                            .mobileLiquidGlass(cornerRadius: 18, elevated: true)
                    }
                }
            }
            .navigationTitle("Overview")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { Task { await vault.reload() } } label: {
                        Image(systemName: "arrow.clockwise")
                    }
                    .disabled(vault.isLoading)
                    .accessibilityLabel("Refresh vault")
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { isComposerPresented = true } label: {
                        Image(systemName: "square.and.pencil")
                    }
                    .accessibilityLabel("New note")
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { isScannerPresented = true } label: {
                        Image(systemName: "doc.viewfinder")
                    }
                    .accessibilityLabel("Scan document")
                }
                }
            }
            .task { recoveredDrafts = await loadDrafts() }
        }
    }

    /// Autosaved drafts that were never finished (app terminated or user
    /// cancelled). One tap reopens them in the composer — nothing typed is
    /// ever lost.
    private var draftRecovery: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label("Unfinished notes", systemImage: "pencil.and.outline")
                .font(.headline)
                .foregroundStyle(MobileTheme.textPrimary)

            ForEach(recoveredDrafts) { draft in
                Button {
                    resumeDraft = draft
                    isComposerPresented = true
                } label: {
                    VStack(alignment: .leading, spacing: 4) {
                        Text(draft.title.isEmpty ? "Untitled note" : draft.title)
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(MobileTheme.textPrimary)
                        Text(String(draft.body.replacingOccurrences(of: "\n", with: " ").prefix(120)))
                            .font(.caption)
                            .foregroundStyle(MobileTheme.textSecondary)
                            .lineLimit(1)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(14)
                    .mobileLiquidGlass(cornerRadius: 16)
                }
                .buttonStyle(.plain)
            }
        }
    }

    private func loadDrafts() async -> [MobileDraftStore.Draft] {
        // Only new-note drafts are surfaced here; edit-mode drafts are
        // reopened directly from the note's Edit action.
        guard let drafts = try? await draftStore?.all() else { return [] }
        return drafts.filter { !$0.id.hasPrefix("edit-") }
    }

    private var overviewHeader: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Good to see your work again.")
                .font(.title2.bold())
                .foregroundStyle(MobileTheme.textPrimary)
			Text(vault.serverURL?.host ?? vault.vaultURL?.lastPathComponent ?? "Vault")
                .font(.subheadline)
                .foregroundStyle(MobileTheme.gold)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var metrics: some View {
        HStack(spacing: 12) {
            MetricCard(value: vault.notes.count, label: "Notes", icon: "note.text")
            MetricCard(value: vault.documents.count, label: "Documents", icon: "doc.text.image")
        }
    }

    @ViewBuilder
    private func section(title: String, systemImage: String, notes: [MobileNote]) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(title, systemImage: systemImage)
                .font(.headline)
                .foregroundStyle(MobileTheme.textPrimary)

            if notes.isEmpty {
                Text("Nothing here yet.")
                    .font(.subheadline)
                    .foregroundStyle(MobileTheme.textMuted)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(18)
                    .mobileLiquidGlass(cornerRadius: 18)
            } else {
                ForEach(notes) { note in
                    NavigationLink {
                        MobileNoteDetailView(noteID: note.id)
                    } label: {
                        MobileNoteRow(note: note)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }
}

private struct MetricCard: View {
    let value: Int
    let label: String
    let icon: String

    var body: some View {
        VStack(alignment: .leading, spacing: 11) {
            Image(systemName: icon)
                .foregroundStyle(MobileTheme.gold)
                .font(.title3)
            Text(value, format: .number)
                .font(.system(size: 30, weight: .bold, design: .rounded))
                .foregroundStyle(MobileTheme.textPrimary)
                .contentTransition(.numericText())
            Text(label)
                .font(.caption.weight(.medium))
                .foregroundStyle(MobileTheme.textSecondary)
        }
        .padding(18)
        .frame(maxWidth: .infinity, alignment: .leading)
        .mobileLiquidGlass(cornerRadius: 20)
    }
}

/// Shows the pending-write queue state on the workspace. Appears whenever
/// writes are queued (offline) or actively uploading; the failed state is
/// surfaced separately in Settings with retry/remove actions.
private struct MobileOutboxBanner: View {
    @EnvironmentObject private var vault: MobileVaultStore

    var body: some View {
        Label {
            Text("\(vault.pendingWriteCount) change\(vault.pendingWriteCount == 1 ? "" : "s") waiting to sync")
        } icon: {
            Image(systemName: "arrow.up.circle.fill")
        }
        .font(.callout.weight(.medium))
        .foregroundStyle(MobileTheme.textPrimary)
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .mobileLiquidGlass(cornerRadius: 16)
        .accessibilityLabel("\(vault.pendingWriteCount) changes waiting to sync")
    }
}

private struct MobileLibraryView: View {
    @EnvironmentObject private var vault: MobileVaultStore
    let documentsOnly: Bool
    @Binding var isComposerPresented: Bool
    @Binding var composerNote: MobileNote?
    @Binding var isScannerPresented: Bool

    @State private var query = ""
    @State private var statusFilter = "All"
    @State private var displayedNotes: [MobileNote] = []

    private var availableStatuses: [String] {
        ["All"] + Array(Set(vault.documents.map(\.status).filter { !$0.isEmpty })).sorted()
    }

    private var request: LibraryRequest {
        LibraryRequest(
            query: query,
            status: statusFilter,
            documentsOnly: documentsOnly,
            revision: vault.revision
        )
    }

    var body: some View {
        NavigationStack {
            MobileBackdrop {
                VStack(spacing: 0) {
                    if documentsOnly, availableStatuses.count > 1 {
                        statusFilters
                    }

                    ScrollView {
                        LazyVStack(spacing: 10) {
                            ForEach(displayedNotes) { note in
                                NavigationLink {
                                    MobileNoteDetailView(noteID: note.id)
                                } label: {
                                    MobileNoteRow(note: note)
                                }
                                .buttonStyle(.plain)
                            }
                        }
                        .padding(.horizontal, 16)
                        .padding(.vertical, 14)
                        .frame(maxWidth: 760)
                        .frame(maxWidth: .infinity)
                    }
                    .refreshable { await vault.reload() }
                    .overlay {
                        if displayedNotes.isEmpty && !vault.isLoading {
                            ContentUnavailableView(
                                query.isEmpty ? (documentsOnly ? "No documents" : "No notes") : "No results",
                                systemImage: query.isEmpty ? "tray" : "magnifyingglass",
                                description: Text(query.isEmpty ? "Pull to refresh the selected vault." : "Try a different title, tag or phrase.")
                            )
                            .foregroundStyle(MobileTheme.textSecondary)
                        } else if vault.isLoading && displayedNotes.isEmpty {
                            ProgressView("Reading vault…")
                        }
                    }
                }
            }
            .navigationTitle(documentsOnly ? "Documents" : "Notes")
            .searchable(text: $query, prompt: documentsOnly ? "Search documents" : "Search notes")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { Task { await vault.reload() } } label: {
                        Image(systemName: "arrow.clockwise")
                    }
                    .disabled(vault.isLoading)
                    .accessibilityLabel("Refresh vault")
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { isScannerPresented = true } label: {
                        Image(systemName: "doc.viewfinder")
                    }
                    .accessibilityLabel("Scan document")
                }
            }
            .task(id: request) { await updateResults(for: request) }
        }
    }

    private var statusFilters: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                ForEach(availableStatuses, id: \.self) { status in
                    Button {
                        statusFilter = status
                    } label: {
                        Text(status == "All" ? status : status.replacingOccurrences(of: "_", with: " ").capitalized)
                            .font(.caption.weight(.semibold))
                            .padding(.horizontal, 13)
                            .padding(.vertical, 8)
                            .foregroundStyle(statusFilter == status ? .black : MobileTheme.textSecondary)
                            .background(
                                statusFilter == status ? MobileTheme.gold : MobileTheme.card,
                                in: Capsule()
                            )
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
        }
        .background(.ultraThinMaterial)
    }

    private func updateResults(for request: LibraryRequest) async {
        if !request.query.isEmpty {
            try? await Task.sleep(for: .milliseconds(170))
        }
        guard !Task.isCancelled else { return }

        let notes = vault.notes
        let normalizedQuery = MobileVaultParser.normalizedSearchQuery(request.query)
        let filtered = await Task.detached(priority: .userInitiated) {
            notes.filter { note in
                guard !request.documentsOnly || note.isDocument else { return false }
                guard request.status == "All" || note.status == request.status else { return false }
                return normalizedQuery.isEmpty || note.searchText.contains(normalizedQuery)
            }
        }.value

        guard !Task.isCancelled else { return }
        displayedNotes = filtered
    }
}

private struct LibraryRequest: Hashable, Sendable {
    let query: String
    let status: String
    let documentsOnly: Bool
    let revision: Int
}

struct MobileNoteRow: View {
    let note: MobileNote

    var body: some View {
        HStack(alignment: .top, spacing: 13) {
            Image(systemName: note.isDocument ? "doc.text.image" : "note.text")
                .font(.headline)
                .foregroundStyle(note.isDocument ? MobileTheme.gold : MobileTheme.ice)
                .frame(width: 38, height: 38)
                .background((note.isDocument ? MobileTheme.gold : MobileTheme.ice).opacity(0.1), in: RoundedRectangle(cornerRadius: 12))
                .accessibilityHidden(true)

            VStack(alignment: .leading, spacing: 6) {
                Text(note.title)
                    .font(.headline)
                    .foregroundStyle(MobileTheme.textPrimary)
                    .lineLimit(2)

                HStack(spacing: 7) {
                    if !note.documentType.isEmpty {
                        Text(note.documentType.capitalized)
                    } else {
                        Text(note.filename)
                    }

                    if !note.status.isEmpty {
                        Text("•")
                        Text(note.status.replacingOccurrences(of: "_", with: " ").capitalized)
                            .foregroundStyle(mobileStatusColor(note.status))
                    }
                }
                .font(.caption)
                .foregroundStyle(MobileTheme.textSecondary)

                if !note.dueDate.isEmpty {
                    Label(note.dueDate, systemImage: "calendar")
                        .font(.caption2.weight(.medium))
                        .foregroundStyle(MobileTheme.goldSoft)
                }
            }

            Spacer(minLength: 8)
            Image(systemName: "chevron.right")
                .font(.caption.bold())
                .foregroundStyle(MobileTheme.textMuted)
                .padding(.top, 11)
                .accessibilityHidden(true)
        }
        .padding(14)
        .mobileLiquidGlass(cornerRadius: 18)
        .contentShape(Rectangle())
    }
}

private struct MobileSettingsView: View {
    @EnvironmentObject private var vault: MobileVaultStore
    @State private var isImporterPresented = false
	@State private var isServerSheetPresented = false

    var body: some View {
        NavigationStack {
            MobileBackdrop {
                ScrollView {
                    VStack(alignment: .leading, spacing: 18) {
                        VStack(alignment: .leading, spacing: 12) {
							Label(vault.isRemote ? "Connected server" : "Current vault", systemImage: vault.isRemote ? "server.rack" : "folder.fill")
                                .font(.headline)
                                .foregroundStyle(MobileTheme.textPrimary)
							Text(vault.serverURL?.host ?? vault.vaultURL?.lastPathComponent ?? "No vault")
                                .font(.title3.bold())
                                .foregroundStyle(MobileTheme.goldSoft)
							Text(vault.displayLocation)
                                .font(.caption.monospaced())
                                .foregroundStyle(MobileTheme.textMuted)
                                .textSelection(.enabled)
                        }
                        .padding(18)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .mobileLiquidGlass(elevated: true)

                        Button {
                            isImporterPresented = true
                        } label: {
                            Label("Choose another vault", systemImage: "folder.badge.gearshape")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .buttonStyle(.bordered)
                        .controlSize(.large)

						Button {
							isServerSheetPresented = true
						} label: {
							Label(vault.isRemote ? "Change server" : "Connect to server", systemImage: "server.rack")
								.frame(maxWidth: .infinity, alignment: .leading)
						}
						.buttonStyle(.bordered)
						.controlSize(.large)

                        Button(role: .destructive) {
                            vault.resetVault()
                        } label: {
                            Label("Disconnect vault", systemImage: "rectangle.portrait.and.arrow.right")
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .buttonStyle(.bordered)
                        .controlSize(.large)

                        VStack(alignment: .leading, spacing: 8) {
							Text(vault.isRemote ? "Self-hosted by design" : "Local-first by design")
                                .font(.headline)
                                .foregroundStyle(MobileTheme.textPrimary)
							Text(vault.isRemote ? "The server owns originals, Markdown, index and the OCR queue. This iPhone downloads a compact snapshot for fast local search and previews originals on demand." : "The iOS app reads Markdown and attachments through the Files permission you grant. Search stays on this device; syncing remains the job of iCloud Drive or your selected file provider.")
                                .font(.subheadline)
                                .foregroundStyle(MobileTheme.textSecondary)
                                .lineSpacing(3)
                        }
                        .padding(18)
                        .mobileLiquidGlass(cornerRadius: 18)

                        if !vault.failedWrites.isEmpty {
                            MobileFailedWritesSection()
                        }
                    }
                    .padding(16)
                    .frame(maxWidth: 680)
                    .frame(maxWidth: .infinity)
                }
            }
            .navigationTitle("Settings")
        }
        .fileImporter(
            isPresented: $isImporterPresented,
            allowedContentTypes: [.folder],
            allowsMultipleSelection: false
        ) { result in
            switch result {
            case .success(let urls):
                if let url = urls.first { vault.selectVault(url) }
            case .failure(let error):
                vault.errorMessage = error.localizedDescription
            }
        }
		.sheet(isPresented: $isServerSheetPresented) {
			MobileServerConnectionView { isServerSheetPresented = false }
		}
    }
}

/// Lists failed writes with their reason and per-entry Retry/Remove
/// actions, so a rejected or conflicted write is never invisible.
private struct MobileFailedWritesSection: View {
    @EnvironmentObject private var vault: MobileVaultStore

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label("Failed writes", systemImage: "exclamationmark.triangle.fill")
                .font(.headline)
                .foregroundStyle(MobileTheme.textPrimary)

            ForEach(vault.failedWrites) { entry in
                VStack(alignment: .leading, spacing: 8) {
                    HStack(spacing: 8) {
                        Image(systemName: "doc.badge.ellipsis")
                            .foregroundStyle(.red)
                        Text(entry.originalFilename ?? URL(fileURLWithPath: entry.path).lastPathComponent)
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(MobileTheme.textPrimary)
                            .lineLimit(1)
                        Spacer()
                    }
                    Text(entry.lastError ?? "Unknown error")
                        .font(.caption)
                        .foregroundStyle(MobileTheme.textSecondary)
                        .fixedSize(horizontal: false, vertical: true)

                    HStack(spacing: 10) {
                        Button {
                            Task { await vault.retryWrite(id: entry.id) }
                        } label: {
                            Label("Retry", systemImage: "arrow.clockwise")
                        }
                        .buttonStyle(.bordered)
                        .controlSize(.small)

                        Button(role: .destructive) {
                            Task { await vault.removeWrite(id: entry.id) }
                        } label: {
                            Label("Remove", systemImage: "trash")
                        }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                    }
                }
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
                .mobileLiquidGlass(cornerRadius: 16)
            }
        }
    }
}

private struct MobileServerConnectionView: View {
	@EnvironmentObject private var vault: MobileVaultStore
	@Environment(\.dismiss) private var dismiss
	@State private var serverURL = ""
	@State private var token = ""
	@State private var isConnecting = false
	@State private var errorMessage: String?
	let connected: () -> Void

	var body: some View {
		NavigationStack {
			MobileBackdrop {
				ScrollView {
					VStack(alignment: .leading, spacing: 20) {
						Label("Your server, native on iPhone", systemImage: "server.rack")
							.font(.title2.bold())
							.foregroundStyle(MobileTheme.textPrimary)
						Text("Connect to the SymDesk container on your Mac mini, Raspberry Pi, NAS or Home Assistant host.")
							.foregroundStyle(MobileTheme.textSecondary)

						VStack(alignment: .leading, spacing: 14) {
							TextField("https://symdesk.example.net", text: $serverURL)
								.textInputAutocapitalization(.never)
								.keyboardType(.URL)
								.textFieldStyle(.roundedBorder)
							SecureField("Access token (32+ characters)", text: $token)
								.textInputAutocapitalization(.never)
								.textFieldStyle(.roundedBorder)
						}
						.padding(18)
						.mobileLiquidGlass(elevated: true)

						if let errorMessage {
							Label(errorMessage, systemImage: "exclamationmark.triangle.fill")
								.font(.callout)
								.foregroundStyle(.red)
						}

						Button {
							Task { await connect() }
						} label: {
							if isConnecting {
								ProgressView().frame(maxWidth: .infinity)
							} else {
								Label("Connect securely", systemImage: "lock.shield").frame(maxWidth: .infinity)
							}
						}
						.buttonStyle(.borderedProminent)
						.tint(MobileTheme.gold)
						.foregroundStyle(.black)
						.disabled(MobileServerConfig.normalizedURL(serverURL) == nil || token.count < 32 || isConnecting)

						Text("Use HTTPS or a trusted VPN outside your home network. The token is stored in Keychain.")
							.font(.footnote)
							.foregroundStyle(MobileTheme.textMuted)
					}
					.padding(20)
				}
			}
			.navigationTitle("Connect server")
			.navigationBarTitleDisplayMode(.inline)
			.toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } } }
		}
	}

	private func connect() async {
		isConnecting = true
		errorMessage = nil
		do {
			try await vault.connectServer(url: serverURL, token: token)
			connected()
			dismiss()
		} catch {
			errorMessage = error.localizedDescription
			isConnecting = false
		}
	}
}
