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
            Button("OK", role: .cancel) { vault.errorMessage = nil }
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
    var body: some View {
        TabView {
            MobileOverviewView()
                .tabItem { Label("Overview", systemImage: "sparkles.rectangle.stack") }

            MobileLibraryView(documentsOnly: false)
                .tabItem { Label("Notes", systemImage: "note.text") }

            MobileLibraryView(documentsOnly: true)
                .tabItem { Label("Documents", systemImage: "doc.text.image") }

            MobileChatView()
                .tabItem { Label("Ask", systemImage: "bubble.left.and.bubble.right") }

            MobileSettingsView()
                .tabItem { Label("Settings", systemImage: "gearshape") }
        }
        .toolbarBackground(.visible, for: .tabBar)
        .toolbarBackground(.ultraThinMaterial, for: .tabBar)
    }
}

private struct MobileOverviewView: View {
    @EnvironmentObject private var vault: MobileVaultStore

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
            }
        }
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
            .searchable(text: $query, prompt: documentsOnly ? "Search documents (tag:name, type:invoice…)" : "Search notes (tag:name, type:invoice…)")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { Task { await vault.reload() } } label: {
                        Image(systemName: "arrow.clockwise")
                    }
                    .disabled(vault.isLoading)
                    .accessibilityLabel("Refresh vault")
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
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(MobileTheme.gold)
                        }
                        .buttonStyle(.plain)
                        .padding(.trailing, 6)
                    }
                    .padding(.horizontal, 16)
                    .padding(.vertical, 8)
                }
                .background(.ultraThinMaterial)
            }

            if documentsOnly, availableStatuses.count > 1 {
                statusFilters
            }

            filterSurface
        }
    }

    private func chip(_ label: String, onRemove: @escaping () -> Void) -> some View {
        HStack(spacing: 6) {
            Text(label)
                .font(.caption.weight(.semibold))
                .foregroundStyle(MobileTheme.textPrimary)
            Button(action: onRemove) {
                Image(systemName: "xmark.circle.fill")
                    .font(.caption)
                    .foregroundStyle(MobileTheme.textMuted)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Remove \(label)")
        }
        .padding(.horizontal, 11)
        .padding(.vertical, 7)
        .background(MobileTheme.card, in: Capsule())
    }

    /// Value pickers for tag / correspondent / document type / date range,
    /// populated from what the vault actually contains.
    private var filterSurface: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 10) {
                filterMenu("Tag", options: facets.tags, selection: $activeFilters.tags, icon: "tag")
                filterMenu("From", options: facets.correspondents, selection: $activeFilters.correspondents, icon: "person.crop.circle")
                filterMenu("Type", options: facets.documentTypes, selection: $activeFilters.documentTypes, icon: "doc.text.image")
                dateRangeMenu
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
        }
        .background(.ultraThinMaterial)
    }

    private func filterMenu(
        _ title: String,
        options: [String],
        selection: Binding<[String]>,
        icon: String
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
                        Label(option, systemImage: "checkmark")
                    } else {
                        Text(option)
                    }
                }
            }
            if options.isEmpty {
                Text("No values in vault")
                    .font(.caption)
                    .foregroundStyle(MobileTheme.textMuted)
            }
        } label: {
            Label(
                selection.wrappedValue.isEmpty ? title : title + " (\(selection.wrappedValue.count))",
                systemImage: icon
            )
            .font(.caption.weight(.semibold))
            .foregroundStyle(MobileTheme.textPrimary)
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .background(MobileTheme.card, in: Capsule())
        }
    }

    private var dateRangeMenu: some View {
        Menu {
            // Quick ranges over the ISO date prefix used by contract-v2.
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
            Label(activeFilters.dateFrom.isEmpty ? "Date" : "Date: \(activeFilters.dateFrom)…", systemImage: "calendar")
                .font(.caption.weight(.semibold))
                .foregroundStyle(MobileTheme.textPrimary)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .background(MobileTheme.card, in: Capsule())
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

                if let snippet, !snippet.isEmpty {
                    Text(snippet)
                        .font(.subheadline)
                        .foregroundStyle(MobileTheme.textSecondary)
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
