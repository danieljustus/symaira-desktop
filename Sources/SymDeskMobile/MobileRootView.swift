import SwiftUI
import SymairaTheme
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
        .background(SymairaTokens.canvas)
        .task {
			if vault.isConfigured, vault.notes.isEmpty {
                await vault.reload()
            }
        }
        .sheet(isPresented: openNoteIsPresented) {
            // Deep link target (Spotlight tap / symdesk:// URL / Handoff /
            // citation tap from the AI chat).
            if let path = vault.pendingOpenPath {
                NavigationStack {
                    MobileNoteDetailView(noteID: path)
                        .toolbar {
                            ToolbarItem(placement: .topBarTrailing) {
                                Button("Done") { vault.pendingOpenPath = nil }
                            }
                        }
                }
                .presentationDetents([.large])
            }
        }
        .alert("Vault unavailable", isPresented: errorIsPresented) {
            Button("Dismiss", role: .cancel) { vault.errorMessage = nil }
        } message: {
            Text(vault.errorMessage ?? "Please try again.")
        }
    }

    private var openNoteIsPresented: Binding<Bool> {
        Binding(
            get: { vault.pendingOpenPath != nil },
            set: { if !$0 { vault.pendingOpenPath = nil } }
        )
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
                        SymairaIconView(.productDesk, size: 34)
                            .foregroundStyle(SymairaTokens.ink2)
                            .accessibilityHidden(true)

                        Text("SymDesk")
                            .symairaText(.display)

                        Text("Your vault. Wherever you are.")
                            .symairaText(.heading)

						Text("Open a local Markdown vault from Files or connect to your self-hosted SymDesk Server.")
                            .font(SymairaTypography.body)
                            .foregroundStyle(SymairaTokens.ink2)
                            .lineSpacing(4)
                    }

                    VStack(spacing: 0) {
                        OnboardingFeature(
                            icon: .cloud,
                            title: "Native iCloud access",
                            detail: "The Files app handles syncing. There is no proprietary cloud database."
                        )
                        Divider().overlay(SymairaTokens.line).padding(.leading, 52)
                        OnboardingFeature(
                            icon: .energy,
                            title: "Fast and offline-ready",
                            detail: "Changed notes are re-read; everything else comes from the local cache."
                        )
                        Divider().overlay(SymairaTokens.line).padding(.leading, 52)
                        OnboardingFeature(
                            icon: .document,
                            title: "Real document previews",
                            detail: "PDFs, images and Office files open with the native Quick Look viewer."
                        )
                    }
                    .padding(.horizontal, 16)
                    .symairaCard(padding: 0)

                    Button {
                        isImporterPresented = true
                    } label: {
                        Label("Choose vault in Files", symairaIcon: .folderAdd)
                            .frame(maxWidth: .infinity)
                    }
                    .symairaButtonStyle(.primary)

                    Button {
                        isServerSheetPresented = true
					} label: {
						Label("Connect to SymDesk Server", symairaIcon: .server)
						    .frame(maxWidth: .infinity)
					}
					.symairaButtonStyle(.secondary)

                    Text("Choose the vault folder itself, not an individual note. SymDesk never uploads your files.")
                        .symairaText(.caption)
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
    let icon: SymairaIcon
    let title: String
    let detail: String

    var body: some View {
        HStack(alignment: .top, spacing: 14) {
            SymairaIconView(icon, size: 20)
                .foregroundStyle(SymairaTokens.ink2)
                .frame(width: 38, height: 38)
                .background(SymairaTokens.raised, in: RoundedRectangle(cornerRadius: SymairaTokens.Radius.md, style: .continuous))
                .accessibilityHidden(true)

            VStack(alignment: .leading, spacing: 3) {
                Text(title)
                    .symairaText(.subheading)
                Text(detail)
                    .symairaText(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 0)
        }
        .padding(.vertical, 14)
    }
}

private struct MobileWorkspaceView: View {
    @EnvironmentObject private var vault: MobileVaultStore
    @Environment(\.scenePhase) private var scenePhase
    @State private var isComposerPresented = false
    @State private var composerNote: MobileNote?
    @State private var resumeDraft: MobileDraftStore.Draft?
    @State private var isScannerPresented = false

    var body: some View {
        TabView {
            MobileOverviewView(isComposerPresented: $isComposerPresented, resumeDraft: $resumeDraft, isScannerPresented: $isScannerPresented)
                .tabItem { Label { Text("Overview") } icon: { Image(symairaIcon: .home, size: 20) } }

            MobileLibraryView(documentsOnly: false, isComposerPresented: $isComposerPresented, composerNote: $composerNote, isScannerPresented: $isScannerPresented)
                .tabItem { Label { Text("Notes") } icon: { Image(symairaIcon: .note, size: 20) } }

            MobileLibraryView(documentsOnly: true, isComposerPresented: $isComposerPresented, composerNote: $composerNote, isScannerPresented: $isScannerPresented)
                .tabItem { Label { Text("Documents") } icon: { Image(symairaIcon: .document, size: 20) } }

            MobileChatView()
                .tabItem { Label { Text("Ask") } icon: { Image(symairaIcon: .chat, size: 20) } }

            MobileSettingsView()
                .tabItem { Label { Text("Settings") } icon: { Image(symairaIcon: .settings, size: 20) } }
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
        .onReceive(NotificationCenter.default.publisher(for: MobileAppActionStore.didSetNotification)) { notification in
            if let action = notification.object as? MobileAppAction {
                present(action)
            }
        }
        .onAppear {
            // A widget tap or quick action may have parked an action while
            // the app was not running; present it on launch.
            if let action = MobileAppActionStore.pending() {
                present(action)
            }
        }
        .onChange(of: scenePhase) { _, phase in
            // Cold launch via widget/quick action: the pending action is
            // read once the scene becomes active.
            if phase == .active, let action = MobileAppActionStore.pending() {
                present(action)
            }
        }
    }

    /// Presents the surface an external action asked for (widget button,
    /// Shortcuts/Siri intent, home-screen quick action) and consumes the
    /// parked action so it does not re-present on the next activation.
    private func present(_ action: MobileAppAction) {
        guard vault.isConfigured else {
            MobileAppActionStore.clear()
            return
        }
        MobileAppActionStore.clear()
        switch action {
        case .newNote:
            composerNote = nil
            resumeDraft = nil
            isComposerPresented = true
        case .scanDocument:
            isScannerPresented = true
        }
    }
}

private struct MobileOverviewView: View {
    @EnvironmentObject private var vault: MobileVaultStore
    @Binding var isComposerPresented: Bool
    @Binding var resumeDraft: MobileDraftStore.Draft?
    @Binding var isScannerPresented: Bool
    @State private var recoveredDrafts: [MobileDraftStore.Draft] = []
    @State private var topography = SymairaTopoModel()
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
            Group {
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
                            section(title: "Recently opened", icon: .time, notes: vault.recentlyOpened)
                        }

                        if !upcomingDocuments.isEmpty {
                            section(title: "Due next", icon: .calendar, notes: upcomingDocuments)
                        }

                        section(title: "Recently changed", icon: .history, notes: recentNotes)

                        if vault.skippedFiles > 0 {
                            SymairaNotice(
                                message: "\(vault.skippedFiles) note\(vault.skippedFiles == 1 ? "" : "s") could not be read. Pull to refresh after iCloud finishes downloading.",
                                tone: .warning
                            )
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
                        SymairaLoadingState("Reading vault…")
                    }
                }
            }
            .symairaTopography(topography)
                .navigationTitle("Overview")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { Task { await vault.reload() } } label: {
                        Image(symairaIcon: .refresh, size: 20)
                    }
                    .disabled(vault.isLoading)
                    .accessibilityLabel("Refresh vault")
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { isComposerPresented = true } label: {
                        Image(symairaIcon: .compose, size: 20)
                    }
                    .accessibilityLabel("New note")
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { isScannerPresented = true } label: {
                        Image(symairaIcon: .scan, size: 20)
                    }
                    .accessibilityLabel("Scan document")
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
            Label("Unfinished notes", symairaIcon: .edit)
                .symairaText(.subheading)

            ForEach(recoveredDrafts) { draft in
                Button {
                    resumeDraft = draft
                    isComposerPresented = true
                } label: {
                    VStack(alignment: .leading, spacing: 4) {
                        Text(draft.title.isEmpty ? "Untitled note" : draft.title)
                            .symairaText(.subheading)
                        Text(String(draft.body.replacingOccurrences(of: "\n", with: " ").prefix(120)))
                            .font(SymairaTypography.caption)
                            .foregroundStyle(SymairaTokens.ink2)
                            .lineLimit(1)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(14)
                    .symairaCard(padding: 0)
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
                .symairaText(.heading)
			Text(vault.serverURL?.host ?? vault.vaultURL?.lastPathComponent ?? "Vault")
                .symairaText(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .topoProtected()
    }

    private var metrics: some View {
        HStack(spacing: 12) {
            MetricCard(value: vault.notes.count, label: "Notes", icon: .note)
            MetricCard(value: vault.documents.count, label: "Documents", icon: .document)
        }
    }

    @ViewBuilder
    private func section(title: String, icon: SymairaIcon, notes: [MobileNote]) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(title, symairaIcon: icon)
                .topoProtected()
                .symairaText(.subheading)

            if notes.isEmpty {
                Text("Nothing here yet.")
                    .symairaText(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(18)
                    .symairaCard(padding: 0)
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
    let icon: SymairaIcon

    var body: some View {
        VStack(alignment: .leading, spacing: 11) {
            SymairaIconView(icon, size: 20)
                .foregroundStyle(SymairaTokens.ink2)
            Text(value, format: .number)
                .font(SymairaTypography.stat)
                .foregroundStyle(SymairaTokens.ink)
                .monospacedDigit()
                .contentTransition(.numericText())
            Text(label)
                .font(SymairaTypography.label)
                .foregroundStyle(SymairaTokens.ink2)
        }
        .padding(18)
        .frame(maxWidth: .infinity, alignment: .leading)
        .symairaCard(padding: 0)
    }
}

/// Shows the pending-write queue state on the workspace. Appears whenever
/// writes are queued (offline) or actively uploading; the failed state is
/// surfaced separately in Settings with retry/remove actions.
private struct MobileOutboxBanner: View {
    @EnvironmentObject private var vault: MobileVaultStore

    var body: some View {
        SymairaNotice(
            message: "\(vault.pendingWriteCount) change\(vault.pendingWriteCount == 1 ? "" : "s") waiting to sync"
        )
    }
}

/// Active filter selection for the library, persisted across relaunches so
/// the user's narrowing survives app restarts (relaunch-persistent chips).
struct MobileActiveFilters: Codable, Equatable, Hashable, Sendable {
    var tags: [String] = []
    var correspondents: [String] = []
    var documentTypes: [String] = []
    var dateFrom: String = ""
    var dateTo: String = ""

    var isActive: Bool {
        !tags.isEmpty || !correspondents.isEmpty || !documentTypes.isEmpty || !dateFrom.isEmpty || !dateTo.isEmpty
    }

    static let storageKey = "symdesk.mobile.active-filters.v1"

    static func load() -> MobileActiveFilters {
        guard let data = UserDefaults.standard.data(forKey: storageKey) else { return MobileActiveFilters() }
        return (try? JSONDecoder().decode(MobileActiveFilters.self, from: data)) ?? MobileActiveFilters()
    }

    func save() {
        guard let data = try? JSONEncoder().encode(self) else { return }
        UserDefaults.standard.set(data, forKey: Self.storageKey)
    }

    var uiFilters: MobileSearchFilterEngine.UIFilters {
        var ui = MobileSearchFilterEngine.UIFilters()
        ui.tags = tags
        ui.correspondents = correspondents
        ui.documentTypes = documentTypes
        if !dateFrom.isEmpty || !dateTo.isEmpty {
            ui.dateRange = (dateFrom.isEmpty ? "0000-01-01" : dateFrom)
                ... (dateTo.isEmpty ? "9999-12-31" : dateTo)
        }
        return ui
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
    @State private var activeFilters = MobileActiveFilters.load()

    /// Search-result snippets keyed by note path (only populated in search mode).
    @State private var snippetsByPath: [String: String] = [:]

    private var availableStatuses: [String] {
        ["All"] + Array(Set(vault.documents.map(\.status).filter { !$0.isEmpty })).sorted()
    }

    private var facets: MobileSearchFilterEngine.Facets {
        MobileSearchFilterEngine.facets(of: vault.notes)
    }

    private var request: LibraryRequest {
        LibraryRequest(
            query: query,
            status: statusFilter,
            documentsOnly: documentsOnly,
            revision: vault.revision,
            filters: activeFilters
        )
    }

    var body: some View {
        NavigationStack {
            MobileBackdrop {
                VStack(spacing: 0) {
                    filterChips

                    ScrollView {
                        LazyVStack(spacing: 10) {
                            ForEach(displayedNotes) { note in
                                NavigationLink {
                                    MobileNoteDetailView(noteID: note.id)
                                } label: {
                                    MobileNoteRow(note: note, snippet: snippetsByPath[note.path])
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
                            SymairaEmptyState(
                                icon: query.isEmpty ? .inbox : .search,
                                title: query.isEmpty ? (documentsOnly ? "No documents" : "No notes") : "No results",
                                message: query.isEmpty ? "Pull to refresh the selected vault." : "Try a different title, tag or phrase."
                            ) {
                                if query.isEmpty {
                                    Button("Refresh vault") { Task { await vault.reload() } }
                                        .symairaButtonStyle(.secondary)
                                }
                            }
                        } else if vault.isLoading && displayedNotes.isEmpty {
                            SymairaLoadingState("Reading vault…")
                        }
                    }
                }
            }
            .navigationTitle(documentsOnly ? "Documents" : "Notes")
            .searchable(text: $query, prompt: documentsOnly ? "Search documents (tag:name, type:invoice…)" : "Search notes (tag:name, type:invoice…)")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { Task { await vault.reload() } } label: {
                        Image(symairaIcon: .refresh, size: 20)
                    }
                    .disabled(vault.isLoading)
                    .accessibilityLabel("Refresh vault")
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { isScannerPresented = true } label: {
                        Image(symairaIcon: .scan, size: 20)
                    }
                    .accessibilityLabel("Scan document")
                }
            }
            .task(id: request) { await updateResults(for: request) }
            .onChange(of: activeFilters) { _, newValue in
                newValue.save()
            }
        }
    }

    /// Removable active-filter chips plus a filter surface for values
    /// derived from the vault's actual content.
    private var filterChips: some View {
        VStack(spacing: 8) {
            if activeFilters.isActive {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        ForEach(activeFilters.tags, id: \.self) { tag in
                            chip("tag: \(tag)") { activeFilters.tags.removeAll { $0 == tag } }
                        }
                        ForEach(activeFilters.correspondents, id: \.self) { correspondent in
                            chip("from: \(correspondent)") { activeFilters.correspondents.removeAll { $0 == correspondent } }
                        }
                        ForEach(activeFilters.documentTypes, id: \.self) { type in
                            chip("type: \(type)") { activeFilters.documentTypes.removeAll { $0 == type } }
                        }
                        if !activeFilters.dateFrom.isEmpty || !activeFilters.dateTo.isEmpty {
                            chip("\(activeFilters.dateFrom)–\(activeFilters.dateTo)") {
                                activeFilters.dateFrom = ""
                                activeFilters.dateTo = ""
                            }
                        }
                        Button {
                            activeFilters = MobileActiveFilters()
                        } label: {
                            Text("Clear all")
                                .font(SymairaTypography.label)
                                .foregroundStyle(SymairaTokens.ink2)
                                .frame(minHeight: SymairaMetrics.minimumControlHeight)
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .padding(.trailing, 6)
                    }
                    .padding(.horizontal, 16)
                    .padding(.vertical, 8)
                }
                .background(SymairaTokens.canvas)
            }

            if documentsOnly, availableStatuses.count > 1 {
                statusFilters
            }

            filterSurface
        }
    }

    private func chip(_ label: String, onRemove: @escaping () -> Void) -> some View {
        HStack(spacing: 0) {
            Text(label)
                .font(SymairaTypography.label)
                .foregroundStyle(SymairaTokens.ink)
            Button(action: onRemove) {
                SymairaIconView(.close, size: 14)
                    .foregroundStyle(SymairaTokens.ink3)
                    .frame(minWidth: SymairaMetrics.minimumControlHeight, minHeight: SymairaMetrics.minimumControlHeight)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Remove \(label)")
        }
        .padding(.leading, 12)
        .background(SymairaTokens.surface, in: Capsule())
    }

    /// Value pickers for tag / correspondent / document type / date range,
    /// populated from what the vault actually contains.
    private var filterSurface: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 10) {
                filterMenu("Tag", options: facets.tags, selection: $activeFilters.tags, icon: .tag)
                filterMenu("From", options: facets.correspondents, selection: $activeFilters.correspondents, icon: .user)
                filterMenu("Type", options: facets.documentTypes, selection: $activeFilters.documentTypes, icon: .document)
                dateRangeMenu
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
        }
        .background(SymairaTokens.canvas)
    }

    private func filterMenu(
        _ title: String,
        options: [String],
        selection: Binding<[String]>,
        icon: SymairaIcon
    ) -> some View {
        Menu {
            ForEach(options, id: \.self) { option in
                Button {
                    if selection.wrappedValue.contains(option) {
                        selection.wrappedValue.removeAll { $0 == option }
                    } else {
                        selection.wrappedValue.append(option)
                    }
                } label: {
                    if selection.wrappedValue.contains(option) {
                        Label { Text(option) } icon: { Image(symairaIcon: .confirm, size: 16) }
                    } else {
                        Text(option)
                    }
                }
            }
            if options.isEmpty {
                Text("No values in vault")
            }
        } label: {
            Label {
                Text(selection.wrappedValue.isEmpty ? title : title + " (\(selection.wrappedValue.count))")
            } icon: {
                Image(symairaIcon: icon, size: 16)
            }
            .font(SymairaTypography.label)
            .foregroundStyle(SymairaTokens.ink)
            .padding(.horizontal, 12)
            .frame(minHeight: SymairaMetrics.minimumControlHeight)
            .background(SymairaTokens.surface, in: Capsule())
        }
    }

    private var dateRangeMenu: some View {
        Menu {
            // Quick ranges over the ISO date prefix retained by contract v6.
            Button("This month") { setMonthRange(offset: 0) }
            Button("Last month") { setMonthRange(offset: -1) }
            Button("This year") {
                let year = String(Calendar.current.component(.year, from: Date()))
                activeFilters.dateFrom = "\(year)-01-01"
                activeFilters.dateTo = "\(year)-12-31"
            }
            Button("Clear date range") {
                activeFilters.dateFrom = ""
                activeFilters.dateTo = ""
            }
        } label: {
            Label {
                Text(activeFilters.dateFrom.isEmpty ? "Date" : "Date: \(activeFilters.dateFrom)…")
            } icon: {
                Image(symairaIcon: .calendar, size: 16)
            }
            .font(SymairaTypography.label)
            .foregroundStyle(SymairaTokens.ink)
            .padding(.horizontal, 12)
            .frame(minHeight: SymairaMetrics.minimumControlHeight)
            .background(SymairaTokens.surface, in: Capsule())
        }
    }

    private func setMonthRange(offset: Int) {
        let calendar = Calendar.current
        let now = Date()
        guard let month = calendar.date(byAdding: .month, value: offset, to: now) else { return }
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyy-MM-dd"
        let first = calendar.date(from: calendar.dateComponents([.year, .month], from: month)) ?? month
        let last = calendar.date(byAdding: DateComponents(month: 1, day: -1), to: first) ?? now
        activeFilters.dateFrom = formatter.string(from: first)
        activeFilters.dateTo = formatter.string(from: last)
    }

    private var statusFilters: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                ForEach(availableStatuses, id: \.self) { status in
                    Button {
                        statusFilter = status
                    } label: {
                        Text(status == "All" ? status : status.replacingOccurrences(of: "_", with: " ").capitalized)
                            .font(SymairaTypography.label)
                            .padding(.horizontal, 13)
                            .frame(minHeight: SymairaMetrics.minimumControlHeight)
                            .foregroundStyle(statusFilter == status ? SymairaTokens.onSelected : SymairaTokens.ink2)
                            .background(
                                statusFilter == status ? SymairaTokens.selected : SymairaTokens.surface,
                                in: Capsule()
                            )
                    }
                    .buttonStyle(.plain)
                    .accessibilityAddTraits(statusFilter == status ? .isSelected : [])
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
        }
        .background(SymairaTokens.canvas)
    }

    private func updateResults(for request: LibraryRequest) async {
        if !request.query.isEmpty {
            try? await Task.sleep(for: .milliseconds(170))
        }
        guard !Task.isCancelled else { return }

        let notes = vault.notes
        let uiFilters = request.filters.uiFilters

        // Parse the operator grammar. On invalid syntax the whole query is
        // degraded to safe plain full-text search — the same policy as the
        // CLI (`searchquery.Parse` callers).
        let plan: MobileSearchQueryParser.Plan
        if let parsed = try? MobileSearchQueryParser.parse(request.query) {
            plan = parsed
        } else if request.query.isEmpty {
            plan = MobileSearchQueryParser.Plan()
        } else {
            plan = MobileSearchQueryParser.Plan(
                terms: [MobileSearchQueryParser.Term(value: request.query, phrase: false, negated: false)]
            )
        }

        // Pure plain-text search (no operators, no chips, no status/side
        // filters) goes through the ranked on-device index: fast prefix
        // search, field-weighted ranking and snippets (#321).
        let isPlainText = plan.filters.isEmpty && plan.regexes.isEmpty
            && !plan.terms.contains(where: { $0.negated || $0.phrase })
        if isPlainText && !request.query.isEmpty && !request.filters.isActive
            && request.status == "All" && !request.documentsOnly {
            let results = await vault.search(request.query)
            guard !Task.isCancelled else { return }
            let byPath = Dictionary(uniqueKeysWithValues: notes.map { ($0.path, $0) })
            let ranked = results.compactMap { result -> (MobileNote, String)? in
                guard let note = byPath[result.path] else { return nil }
                let snippet = MobileSearchSnippet.snippet(
                    for: note.body,
                    normalizedQuery: MobileVaultParser.normalizedSearchQuery(request.query)
                )
                return (note, snippet)
            }
            snippetsByPath = Dictionary(uniqueKeysWithValues: ranked.map { ($0.0.path, $0.1) })
            displayedNotes = ranked.map(\.0)
            return
        }

        // Operators, chips or browsing mode: one predicate engine for both
        // typed operators and chip selections (shared contract §2).
        let filtered = await Task.detached(priority: .userInitiated) {
            MobileSearchFilterEngine.filter(notes, plan: plan, ui: uiFilters).filter { note in
                guard !request.documentsOnly || note.isDocument else { return false }
                guard request.status == "All" || note.status == request.status else { return false }
                return true
            }
        }.value
        guard !Task.isCancelled else { return }

        if request.query.isEmpty {
            snippetsByPath = [:]
        } else {
            let plainQuery = plan.terms.map(\.value).joined(separator: " ")
            snippetsByPath = Dictionary(uniqueKeysWithValues: filtered.compactMap { note in
                let snippet = MobileSearchSnippet.snippet(for: note.body, normalizedQuery: plainQuery)
                return snippet.isEmpty ? nil : (note.path, snippet)
            })
        }
        displayedNotes = filtered
    }
}

private struct LibraryRequest: Hashable, Sendable {
    let query: String
    let status: String
    let documentsOnly: Bool
    let revision: Int
    let filters: MobileActiveFilters
}

struct MobileNoteRow: View {
    let note: MobileNote
    /// Optional search-result snippet; shown instead of the first line
    /// when the row came from a ranked query.
    var snippet: String? = nil

    var body: some View {
        HStack(alignment: .top, spacing: 13) {
            SymairaIconView(note.isDocument ? .document : .note, size: 18)
                .foregroundStyle(SymairaTokens.ink2)
                .frame(width: 38, height: 38)
                .background(SymairaTokens.raised, in: RoundedRectangle(cornerRadius: SymairaTokens.Radius.md, style: .continuous))
                .accessibilityHidden(true)

            VStack(alignment: .leading, spacing: 6) {
                Text(note.title)
                    .symairaText(.subheading)
                    .lineLimit(2)

                if let snippet, !snippet.isEmpty {
                    Text(snippet)
                        .symairaText(.secondary)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                }

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
                .font(SymairaTypography.caption)
                .foregroundStyle(SymairaTokens.ink2)

                if !note.dueDate.isEmpty {
                    Label(note.dueDate, symairaIcon: .calendar)
                        .font(SymairaTypography.label)
                        .foregroundStyle(SymairaTokens.ink)
                }
            }

            Spacer(minLength: 8)
            SymairaIconView(.forward, size: 14)
                .foregroundStyle(SymairaTokens.ink3)
                .padding(.top, 11)
                .accessibilityHidden(true)
        }
        .padding(14)
        .symairaCard(padding: 0)
        .contentShape(Rectangle())
    }
}

private struct MobileSettingsView: View {
    @EnvironmentObject private var vault: MobileVaultStore
    @AppStorage("appearance") private var appearance: SymairaAppearance = .system
    @State private var isDisconnectConfirmationPresented = false
    @State private var isImporterPresented = false
	@State private var isServerSheetPresented = false

    var body: some View {
        NavigationStack {
            MobileBackdrop {
                ScrollView {
                    VStack(alignment: .leading, spacing: SymairaSpacing.xLarge) {
                        SymairaFormSection(vault.isRemote ? "Connected server" : "Current vault") {
                            VStack(alignment: .leading, spacing: SymairaSpacing.small) {
                                Text(vault.serverURL?.host ?? vault.vaultURL?.lastPathComponent ?? "No vault")
                                    .symairaText(.heading)
                                Text(vault.displayLocation)
                                    .symairaText(.monoSmall)
                                    .textSelection(.enabled)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                        }

                        SymairaFormSection("Appearance") {
                            SymairaAppearancePicker(selection: $appearance)
                        }

                        Button {
                            isImporterPresented = true
                        } label: {
                            Label("Choose another vault", symairaIcon: .folder)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .symairaButtonStyle(.secondary)

						Button {
							isServerSheetPresented = true
						} label: {
							Label(vault.isRemote ? "Change server" : "Connect to server", symairaIcon: .server)
							    .frame(maxWidth: .infinity, alignment: .leading)
						}
						.symairaButtonStyle(.secondary)

                        Button(role: .destructive) {
                            isDisconnectConfirmationPresented = true
                        } label: {
                            Label("Disconnect vault", symairaIcon: .logout)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .symairaButtonStyle(.secondary)
                        .confirmationDialog("Disconnect this vault?", isPresented: $isDisconnectConfirmationPresented, titleVisibility: .visible) {
                            Button("Disconnect vault", role: .destructive) { vault.resetVault() }
                        }

                        SymairaFormSection(vault.isRemote ? "Self-hosted by design" : "Local-first by design") {
							Text(vault.isRemote ? "The server owns originals, Markdown, index and the OCR queue. This iPhone downloads a compact snapshot for fast local search and previews originals on demand." : "The iOS app reads Markdown and attachments through the Files permission you grant. Search stays on this device; syncing remains the job of iCloud Drive or your selected file provider.")
                                .symairaText(.body)
                                .lineSpacing(3)
                        }

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
            Label("Failed writes", symairaIcon: .warning)
                .symairaText(.subheading)

            ForEach(vault.failedWrites) { entry in
                VStack(alignment: .leading, spacing: 8) {
                    HStack(spacing: 8) {
                        SymairaIconView(.document, size: 16)
                            .foregroundStyle(SymairaTokens.critical)
                        Text(entry.originalFilename ?? URL(fileURLWithPath: entry.path).lastPathComponent)
                            .symairaText(.subheading)
                            .lineLimit(1)
                        Spacer()
                    }
                    Text(entry.lastError ?? "Unknown error")
                        .font(SymairaTypography.caption)
                        .foregroundStyle(SymairaTokens.ink2)
                        .fixedSize(horizontal: false, vertical: true)

                    HStack(spacing: 10) {
                        Button {
                            Task { await vault.retryWrite(id: entry.id) }
                        } label: {
                            Label("Retry", symairaIcon: .refresh)
                        }
                        .symairaButtonStyle(.secondary)

                        Button(role: .destructive) {
                            Task { await vault.removeWrite(id: entry.id) }
                        } label: {
                            Label("Remove", symairaIcon: .delete)
                        }
                        .symairaButtonStyle(.secondary)
                    }
                }
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
                .symairaCard(padding: 0)
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
						Label("Your server, native on iPhone", symairaIcon: .server)
						    .symairaText(.heading)
						Text("Connect to the SymDesk container on your Mac mini, Raspberry Pi, NAS or Home Assistant host.")
						    .symairaText(.secondary)

						VStack(alignment: .leading, spacing: 14) {
							TextField("https://symdesk.example.net", text: $serverURL)
								.textInputAutocapitalization(.never)
								.keyboardType(.URL)
								.textFieldStyle(.symaira)
							SecureField("Access token (32+ characters)", text: $token)
								.textInputAutocapitalization(.never)
								.textFieldStyle(.symaira)
						}
						.padding(18)
						.symairaCard(padding: 0)

						if let errorMessage {
							SymairaNotice(message: errorMessage, tone: .critical, onDismiss: { self.errorMessage = nil })
						}

						Button {
							Task { await connect() }
						} label: {
							if isConnecting {
							    HStack(spacing: SymairaSpacing.small) {
							        ProgressView()
							        Text("Connecting…")
							    }
							    .frame(maxWidth: .infinity)
							} else {
							    Label("Connect securely", symairaIcon: .shieldCheck).frame(maxWidth: .infinity)
							}
						}
						.symairaButtonStyle(.primary)
						.disabled(MobileServerConfig.normalizedURL(serverURL) == nil || token.count < 32 || isConnecting)

						Text("Use HTTPS or a trusted VPN outside your home network. The token is stored in Keychain.")
							.symairaText(.caption)
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
