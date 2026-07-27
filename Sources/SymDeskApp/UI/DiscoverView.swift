import SwiftUI
import SymairaTheme
import SymDeskCore

// MARK: - Discover Card Model

struct DiscoverCard: Identifiable {
    let id: String
    let group: Group
    let title: String
    let description: String
    let icon: String
    let actionLabel: String
    let action: CardAction

    enum Group: String, CaseIterable, Identifiable {
        case findSearch = "Find & search"
        case captureAutomatic = "Capture automatically"
        case automateAgents = "Automate & agents"
        case ownYourData = "Own your data"

        var id: String { rawValue }
    }

    enum CardAction: Sendable {
        case openPanel
        case revealFolder
        case copyCommand(String)
        case openDocs(URL)
        case noop
    }
}

// MARK: - DiscoverView

struct DiscoverView: View {
    @EnvironmentObject var core: DeskCore
    @Environment(\.openURL) private var openURL

    @AppStorage("discoverExploredIDs") private var exploredIDsRaw: String = "[]"

    @State private var exploredIDs: Set<String> = []
    @State private var doctorReport: DoctorReport?
    @State private var isLoadingDoctor = true

    private var allCards: [DiscoverCard] {
        DiscoverCardCatalog.allCards(doctorReport: doctorReport)
    }

    private var totalCards: Int { allCards.count }
    private var exploredCount: Int {
        allCards.filter { exploredIDs.contains($0.id) }.count
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                headerSection

                if isLoadingDoctor {
                    ProgressView("Checking installed tools…")
                        .tint(SymairaTheme.goldPrimary)
                        .foregroundColor(SymairaTheme.textSecondary)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 40)
                } else {
                    ForEach(DiscoverCard.Group.allCases) { group in
                        let cards = allCards.filter { $0.group == group }
                        if !cards.isEmpty {
                            groupSection(group: group, cards: cards)
                        }
                    }
                }
            }
            .padding(28)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .navigationTitle("Discover")
        .onAppear {
            exploredIDs = Self.loadExploredIDs(from: exploredIDsRaw)
        }
        .onChange(of: exploredIDs) { newValue in
            Self.saveExploredIDs(newValue, to: &exploredIDsRaw)
        }
        .task {
            await loadDoctor()
        }
    }

    // MARK: - Header

    private var headerSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("SymDesk Capabilities")
                .font(.title2.bold())
                .foregroundColor(SymairaTheme.textPrimary)

            Text("Explore what your vault can do. Mark each capability as explored to track your progress.")
                .font(.callout)
                .foregroundColor(SymairaTheme.textSecondary)

            if !isLoadingDoctor {
                ProgressView(value: Double(exploredCount), total: Double(max(totalCards, 1))) {
                    Text("\(exploredCount) of \(totalCards) explored")
                        .font(.caption.bold())
                        .foregroundColor(SymairaTheme.textSecondary)
                }
                .progressViewStyle(.linear)
                .tint(SymairaTheme.goldPrimary)
                .frame(maxWidth: 320)
                .padding(.top, 4)
            }
        }
    }

    // MARK: - Group Section

    private func groupSection(group: DiscoverCard.Group, cards: [DiscoverCard]) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(group.rawValue)
                .font(.headline)
                .foregroundColor(SymairaTheme.goldPrimary)

            LazyVGrid(columns: [GridItem(.adaptive(minimum: 260, maximum: 400), spacing: 14)], spacing: 14) {
                ForEach(cards) { card in
                    DiscoverCardView(
                        card: card,
                        isExplored: exploredIDs.contains(card.id)
                    ) {
                        markExplored(card.id)
                        performAction(card.action)
                    }
                }
            }
        }
    }

    // MARK: - Actions

    private func markExplored(_ id: String) {
        exploredIDs.insert(id)
    }

    private static func loadExploredIDs(from raw: String) -> Set<String> {
        guard let data = raw.data(using: .utf8),
              let ids = try? JSONDecoder().decode(Set<String>.self, from: data) else {
            return []
        }
        return ids
    }

    private static func saveExploredIDs(_ ids: Set<String>, to raw: inout String) {
        if let data = try? JSONEncoder().encode(ids),
           let str = String(data: data, encoding: .utf8) {
            raw = str
        }
    }

    private func performAction(_ action: DiscoverCard.CardAction) {
        switch action {
        case .openPanel:
            openDocumentsFolder()
        case .revealFolder:
            revealVaultFolder()
        case .copyCommand(let cmd):
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(cmd, forType: .string)
        case .openDocs(let url):
            openURL(url)
        case .noop:
            break
        }
    }

    private func openDocumentsFolder() {
        if let vaultPath = core.vaultPath {
            let url = URL(fileURLWithPath: vaultPath)
            NSWorkspace.shared.open(url)
        }
    }

    private func revealVaultFolder() {
        if let vaultPath = core.vaultPath {
            let url = URL(fileURLWithPath: vaultPath)
            NSWorkspace.shared.activateFileViewerSelecting([url])
        }
    }

    private func loadDoctor() async {
        isLoadingDoctor = true
        do {
            doctorReport = try await core.getDoctorReport()
        } catch {
            doctorReport = nil
        }
        isLoadingDoctor = false
    }
}

// MARK: - Card View

private struct DiscoverCardView: View {
    let card: DiscoverCard
    let isExplored: Bool
    let onExplore: () -> Void

    @State private var isHovering = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: card.icon)
                    .font(.title2)
                    .foregroundColor(isExplored ? SymairaTheme.iceSecondary : SymairaTheme.goldPrimary)
                    .frame(width: 28, height: 28)

                VStack(alignment: .leading, spacing: 3) {
                    Text(card.title)
                        .font(.body.weight(.semibold))
                        .foregroundColor(SymairaTheme.textPrimary)
                    Text(card.description)
                        .font(.caption)
                        .foregroundColor(SymairaTheme.textSecondary)
                        .lineLimit(3)
                }

                Spacer()

                if isExplored {
                    Image(systemName: "checkmark.circle.fill")
                        .foregroundColor(SymairaTheme.goldPrimary)
                        .font(.title3)
                }
            }

            Divider()
                .overlay(SymairaTheme.borderGlass)

            HStack {
                Spacer()
                Button(action: onExplore) {
                    Text(isExplored ? "Visited" : card.actionLabel)
                        .font(.caption.weight(.medium))
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .disabled(isExplored)
            }
        }
        .padding(14)
        .glassCard(isHovered: isHovering)
        .onHover { hovering in
            withAnimation(SymairaTheme.transitionFast) {
                isHovering = hovering
            }
        }
    }
}

// MARK: - Card Catalog

enum DiscoverCardCatalog {
    static func allCards(doctorReport: DoctorReport?) -> [DiscoverCard] {
        var cards: [DiscoverCard] = []

        // ── Find & search ──────────────────────────────────
        cards.append(DiscoverCard(
            id: "doc-grid",
            group: .findSearch,
            title: "Document Grid",
            description: "Browse your entire vault as a visual grid of document cards. Sort by date, status, or type — like a paperless dashboard.",
            icon: "square.grid.2x2",
            actionLabel: "Open Grid",
            action: .openPanel
        ))

        cards.append(DiscoverCard(
            id: "fulltext-search",
            group: .findSearch,
            title: "Full-Text Search",
            description: "Instantly search every note and document in your vault. Results show snippets so you can judge relevance before opening.",
            icon: "magnifyingglass",
            actionLabel: "Try Search",
            action: .openPanel
        ))

        cards.append(DiscoverCard(
            id: "backlinks",
            group: .findSearch,
            title: "Backlinks",
            description: "Every note shows which other notes reference it. Discover connections you didn't know existed across your vault.",
            icon: "arrow.triangle.branch",
            actionLabel: "View Notes",
            action: .openPanel
        ))

        cards.append(DiscoverCard(
            id: "graph",
            group: .findSearch,
            title: "Link Graph",
            description: "Visualize your vault as an interactive graph. See clusters, orphan notes, and the structure of your knowledge.",
            icon: "point.3.connected.trianglepath.dotted",
            actionLabel: "Open Graph",
            action: .openPanel
        ))

        // ── Capture automatically ───────────────────────────
        cards.append(DiscoverCard(
            id: "watch-folder",
            group: .captureAutomatic,
            title: "Watch Folder",
            description: "SymDesk watches your vault and automatically detects file changes. New notes appear instantly — no manual refresh.",
            icon: "eye.fill",
            actionLabel: "Reveal Vault",
            action: .revealFolder
        ))

        cards.append(DiscoverCard(
            id: "drag-drop-ingest",
            group: .captureAutomatic,
            title: "Drag & Drop Ingest",
            description: "Drop any PDF, image, or file onto the app window. It gets copied into your inbox and indexed automatically.",
            icon: "arrow.down.doc",
            actionLabel: "Open Inbox",
            action: .openPanel
        ))

        cards.append(DiscoverCard(
            id: "pdf-viewer",
            group: .captureAutomatic,
            title: "PDF Viewer",
            description: "View PDFs inline without leaving the app. Preview scanned documents and receipts side-by-side with your notes.",
            icon: "doc.richtext",
            actionLabel: "Open Docs",
            action: .openPanel
        ))

        // ── Automate & agents ──────────────────────────────
        cards.append(DiscoverCard(
            id: "cli",
            group: .automateAgents,
            title: "CLI Access",
            description: "Run `symdesk` from Terminal to index, search, create notes, or query your vault. Every feature is scriptable.",
            icon: "terminal",
            actionLabel: "Copy Command",
            action: .copyCommand("symdesk --help")
        ))

        cards.append(DiscoverCard(
            id: "mcp-server",
            group: .automateAgents,
            title: "MCP Server",
            description: "Connect any AI agent (Claude, GPT, Codex) to your vault via the Model Context Protocol. Agents can search, read, and create notes.",
            icon: "cpu",
            actionLabel: "Copy Command",
            action: .copyCommand("symdesk mcp")
        ))

        cards.append(DiscoverCard(
            id: "ai-ask",
            group: .automateAgents,
            title: "AI-Powered Answers",
            description: "Ask natural-language questions about your vault. SymDesk searches relevant documents and generates grounded answers using local LLMs.",
            icon: "sparkles",
            actionLabel: "Copy Command",
            action: .copyCommand("symdesk ask \"your question\"")
        ))

        // ── Own your data ──────────────────────────────────
        cards.append(DiscoverCard(
            id: "open-format",
            group: .ownYourData,
            title: "Open Format",
            description: "Your vault is plain Markdown files with YAML frontmatter. No proprietary format, no lock-in — just files you can open anywhere.",
            icon: "doc.plaintext",
            actionLabel: "Reveal Vault",
            action: .revealFolder
        ))

        cards.append(DiscoverCard(
            id: "local-first",
            group: .ownYourData,
            title: "Local-First Storage",
            description: "Everything lives on your Mac. The SQLite sidecar index, your notes, your documents — no cloud dependency required.",
            icon: "internaldrive",
            actionLabel: "Reveal Vault",
            action: .revealFolder
        ))

        cards.append(DiscoverCard(
            id: "icloud-sync",
            group: .ownYourData,
            title: "iCloud Sync",
            description: "Store your vault in iCloud Drive and it syncs automatically across all your Macs. SymDesk handles conflicts gracefully.",
            icon: "icloud",
            actionLabel: "Open Docs",
            action: .openDocs(URL(string: "https://github.com/danieljustus/symaira-desktop") ?? URL(string: "https://apple.com")!)
        ))

        // ── Composition / upgrade cards (reflect live doctor status) ──
        if let report = doctorReport {
            let toolStatus = report.tools

            cards.append(DiscoverCard(
                id: "compose-symseek",
                group: .findSearch,
                title: "SymSeek (Search Engine)",
                description: toolStatus.isAvailable("symseek")
                    ? "SymSeek is installed and providing full-text search across your vault."
                    : "SymSeek is not on PATH. Install it to enable fast full-text search.",
                icon: toolStatus.isAvailable("symseek") ? "checkmark.seal.fill" : "exclamationmark.triangle",
                actionLabel: toolStatus.isAvailable("symseek") ? "Installed" : "Install",
                action: toolStatus.isAvailable("symseek") ? .noop : .openDocs(URL(string: "https://github.com/danieljustus/symaira-seek") ?? URL(string: "https://apple.com")!)
            ))

            cards.append(DiscoverCard(
                id: "compose-symmemory",
                group: .automateAgents,
                title: "SymMemory (RAG/Graph)",
                description: report.tools.isAvailable("symmemory")
                    ? "SymMemory is installed — vector search and knowledge graph are active."
                    : "SymMemory is not on PATH. Install it for RAG and graph capabilities.",
                icon: report.tools.isAvailable("symmemory") ? "checkmark.seal.fill" : "exclamationmark.triangle",
                actionLabel: report.tools.isAvailable("symmemory") ? "Installed" : "Install",
                action: report.tools.isAvailable("symmemory") ? .noop : .openDocs(URL(string: "https://github.com/danieljustus/symaira-memory") ?? URL(string: "https://apple.com")!)
            ))

            cards.append(DiscoverCard(
                id: "compose-symingest",
                group: .captureAutomatic,
                title: "SymIngest (OCR)",
                description: report.tools.isAvailable("symingest")
                    ? "SymIngest is installed — OCR and document ingestion are active."
                    : "SymIngest is not on PATH. Install it to enable OCR on scanned documents.",
                icon: report.tools.isAvailable("symingest") ? "checkmark.seal.fill" : "exclamationmark.triangle",
                actionLabel: report.tools.isAvailable("symingest") ? "Installed" : "Install",
                action: report.tools.isAvailable("symingest") ? .noop : .openDocs(URL(string: "https://github.com/danieljustus/symaira-ingest") ?? URL(string: "https://apple.com")!)
            ))
        }

        return cards
    }
}
