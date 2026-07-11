import SwiftUI
import SymairaTheme
import SymDeskCore

struct DocumentGridView: View {
    @EnvironmentObject var core: DeskCore
    @EnvironmentObject var watcher: EventWatcher

    let statusFilter: String?
    let deepLinkPath: String?

    @State private var documents: [DocumentItem] = []
    @State private var searchText = ""
    @State private var isLoading = false
    @State private var selectedDoc: DocumentItem?
    @State private var showAgentSheet = false
    @State private var agentTarget: DocumentItem?
    @State private var agentQuery = ""
    @State private var agentResult = ""
    @State private var isAgentRunning = false
    @State private var openDoc: DocumentItem?
    @State private var sortByASN = false
    @State private var selectedPaths: Set<String> = []
    @State private var selectionAnchor: String?
    @State private var batchSummary: String?

    private var selectedDocs: [DocumentItem] {
        filteredDocs.filter { selectedPaths.contains($0.path) }
    }

    var filteredDocs: [DocumentItem] {
        var result = documents
        if let s = statusFilter, !s.isEmpty {
            result = result.filter { $0.status == s }
        }
        if !searchText.isEmpty {
            let q = searchText.lowercased()
            result = result.filter {
                $0.title.lowercased().contains(q)
                || $0.correspondent.lowercased().contains(q)
                || $0.person.lowercased().contains(q)
                || $0.documentType.lowercased().contains(q)
                || ($0.asn > 0 && String($0.asn).contains(q))
            }
        }
        if sortByASN {
            result.sort {
                switch ($0.asn > 0, $1.asn > 0) {
                case (true, true): return $0.asn == $1.asn ? $0.title < $1.title : $0.asn < $1.asn
                case (true, false): return true
                case (false, true): return false
                case (false, false): return $0.title < $1.title
                }
            }
        }
        return result
    }

    var body: some View {
        VStack(spacing: 0) {
            searchField
            Divider()
            if isLoading {
                ProgressView("Loading documents…")
                    .tint(SymairaTheme.goldPrimary)
                    .foregroundColor(SymairaTheme.textSecondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if filteredDocs.isEmpty {
                emptyState
            } else {
                gridContent
            }
        }
        .task { await fetchDocs() }
        .task(id: deepLinkPath) {
            guard let path = deepLinkPath, !path.isEmpty else { return }
            if let doc = documents.first(where: { $0.path == path }) {
                openDoc = doc
            }
        }
        .onChange(of: watcher.latestEvent) {
            Task { await fetchDocs() }
        }
        .sheet(isPresented: $showAgentSheet) {
            agentSheet
        }
        .sheet(item: $openDoc) { doc in
            DocumentViewerView(document: doc)
        }
        .alert("Batch Action", isPresented: Binding(
            get: { batchSummary != nil },
            set: { if !$0 { batchSummary = nil } }
        )) {
            Button("OK", role: .cancel) { batchSummary = nil }
        } message: {
            Text(batchSummary ?? "")
        }
    }

    // MARK: - Search Field

    private var searchField: some View {
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass")
                .foregroundColor(SymairaTheme.goldPrimary)
            TextField("Search or ask…", text: $searchText)
                .textFieldStyle(.plain)
                .foregroundColor(SymairaTheme.textPrimary)
                .onSubmit {
                    if !searchText.isEmpty {
                        agentTarget = nil
                        agentQuery = searchText
                        showAgentSheet = true
                    }
                }
            if !searchText.isEmpty {
                Button(action: { searchText = "" }) {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundColor(SymairaTheme.textMuted)
                }
                .buttonStyle(.plain)
            }
            if selectedPaths.count > 1 {
                HStack(spacing: 4) {
                    Text("\(selectedPaths.count) selected")
                        .font(.caption)
                        .foregroundColor(SymairaTheme.goldPrimary)
                    Button(action: { clearSelection() }) {
                        Image(systemName: "xmark.circle")
                            .foregroundColor(SymairaTheme.textMuted)
                    }
                    .buttonStyle(.plain)
                    .help("Clear selection")
                }
            }
            Button(action: { sortByASN.toggle() }) {
                Label(sortByASN ? "Default order" : "Sort by ASN", systemImage: "number")
                    .labelStyle(.iconOnly)
                    .foregroundColor(sortByASN ? SymairaTheme.goldPrimary : SymairaTheme.textMuted)
            }
            .buttonStyle(.plain)
            .help(sortByASN ? "Use default order" : "Sort by archive serial number")
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(SymairaTheme.bgCard)
        .cornerRadius(8)
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(SymairaTheme.borderGlass, lineWidth: 1)
        )
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
    }

    // MARK: - Empty State

    private var emptyState: some View {
        VStack(spacing: 12) {
            Image(systemName: "doc.text.magnifyingglass")
                .font(.system(size: 48))
                .foregroundColor(SymairaTheme.textMuted)
            Text("No documents found")
                .font(.title3)
                .foregroundColor(SymairaTheme.textSecondary)
            if !searchText.isEmpty {
                Text("Try a different search term or press Enter to ask the AI")
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    // MARK: - Grid

    private var gridContent: some View {
        ScrollView([.horizontal, .vertical]) {
            LazyVGrid(
                columns: [GridItem(.adaptive(minimum: 220, maximum: 320), spacing: 16)],
                alignment: .leading,
                spacing: 16
            ) {
                ForEach(filteredDocs) { doc in
                    DocumentCard(doc: doc, isSelected: selectedDoc?.id == doc.id || selectedPaths.contains(doc.path))
                        .onTapGesture(count: 2) { openDoc = doc }
                        .onTapGesture { handleTap(doc: doc) }
                        .contextMenu {
                            if selectedPaths.contains(doc.path), selectedPaths.count > 1 {
                                batchContextMenu(docs: selectedDocs)
                            } else {
                                documentContextMenu(doc: doc)
                            }
                        }
                }
            }
            .padding(16)
        }
        .background {
            // Invisible buttons providing selection keyboard shortcuts.
            Group {
                Button("") { selectAllInFilter() }
                    .keyboardShortcut("a", modifiers: .command)
                Button("") { clearSelection() }
                    .keyboardShortcut(.escape, modifiers: [])
            }
            .opacity(0)
            .frame(width: 0, height: 0)
        }
    }

    // MARK: - Selection

    private func handleTap(doc: DocumentItem) {
        let modifiers = NSApp.currentEvent?.modifierFlags ?? []
        if modifiers.contains(.command) {
            if selectedPaths.contains(doc.path) {
                selectedPaths.remove(doc.path)
            } else {
                selectedPaths.insert(doc.path)
                selectionAnchor = doc.path
            }
        } else if modifiers.contains(.shift), let anchor = selectionAnchor,
                  let anchorIdx = filteredDocs.firstIndex(where: { $0.path == anchor }),
                  let docIdx = filteredDocs.firstIndex(where: { $0.path == doc.path }) {
            let range = min(anchorIdx, docIdx)...max(anchorIdx, docIdx)
            selectedPaths.formUnion(filteredDocs[range].map(\.path))
        } else {
            selectedPaths = [doc.path]
            selectionAnchor = doc.path
        }
        selectedDoc = doc
    }

    private func selectAllInFilter() {
        selectedPaths = Set(filteredDocs.map(\.path))
        selectionAnchor = filteredDocs.first?.path
    }

    private func clearSelection() {
        selectedPaths = []
        selectionAnchor = nil
    }

    // MARK: - Batch Context Menu

    private static let commonDocumentTypes = ["invoice", "receipt", "contract", "letter", "tax", "insurance"]

    @ViewBuilder
    private func batchContextMenu(docs: [DocumentItem]) -> some View {
        Text("\(docs.count) documents selected")
        Divider()
        Menu("Set Status") {
            ForEach(DocumentStatus.allCases) { status in
                Button(action: {
                    runBatch { try await core.docSetStatus(paths: docs.map(\.path), status: status.rawValue) }
                }) {
                    Label(status.label, systemImage: status.systemImage)
                }
            }
        }
        Menu("Set Type") {
            ForEach(Self.commonDocumentTypes, id: \.self) { type in
                Button(type.capitalized) {
                    runBatch { try await core.docSetType(paths: docs.map(\.path), type: type) }
                }
            }
            Divider()
            Button("Custom…") {
                if let type = promptForText(title: "Set Document Type", message: "Type for \(docs.count) documents:") {
                    runBatch { try await core.docSetType(paths: docs.map(\.path), type: type) }
                }
            }
        }
        Button("Set Correspondent…") {
            if let name = promptForText(title: "Set Correspondent", message: "Correspondent for \(docs.count) documents:") {
                runBatch { try await core.docSetCorrespondent(paths: docs.map(\.path), name: name) }
            }
        }
        Button("Set Due Date…") {
            if let date = promptForText(title: "Set Due Date", message: "Due date (YYYY-MM-DD) for \(docs.count) documents:") {
                runBatch { try await core.docSetDue(paths: docs.map(\.path), date: date) }
            }
        }
        Menu("Tags") {
            Button("Add Tag…") {
                if let tag = promptForText(title: "Add Tag", message: "Tag to add to \(docs.count) documents:") {
                    runBatch { try await core.docAddTag(paths: docs.map(\.path), tag: tag) }
                }
            }
            Button("Remove Tag…") {
                if let tag = promptForText(title: "Remove Tag", message: "Tag to remove from \(docs.count) documents:") {
                    runBatch { try await core.docRemoveTag(paths: docs.map(\.path), tag: tag) }
                }
            }
        }
        Divider()
        Button("Deselect All") { clearSelection() }
    }

    /// Runs a batch mutation, refreshes the grid and surfaces per-file failures.
    private func runBatch(_ operation: @escaping () async throws -> DeskCore.DocBatchOutcome) {
        Task {
            do {
                let outcome = try await operation()
                await fetchDocs()
                if outcome.failed > 0 {
                    let failures = outcome.results
                        .filter { $0.status == "error" }
                        .map { "\($0.file): \($0.error ?? "unknown error")" }
                        .joined(separator: "\n")
                    batchSummary = "\(outcome.updated) updated, \(outcome.failed) failed:\n\(failures)"
                } else {
                    batchSummary = nil
                }
            } catch {
                batchSummary = "Batch action failed: \(error.localizedDescription)"
            }
        }
    }

    /// Modal single-line text prompt (NSAlert with an accessory text field).
    private func promptForText(title: String, message: String) -> String? {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = message
        alert.addButton(withTitle: "OK")
        alert.addButton(withTitle: "Cancel")
        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 240, height: 24))
        alert.accessoryView = field
        alert.window.initialFirstResponder = field
        guard alert.runModal() == .alertFirstButtonReturn else { return nil }
        let value = field.stringValue.trimmingCharacters(in: .whitespaces)
        return value.isEmpty ? nil : value
    }

    // MARK: - Context Menu

    @ViewBuilder
    private func documentContextMenu(doc: DocumentItem) -> some View {
        Button(action: { openDoc = doc }) {
            Label("Open", systemImage: "doc.text")
        }
        .keyboardShortcut("o", modifiers: .command)
        Divider()
        statusSubmenu(doc: doc)
        Divider()
        tagsSubmenu(doc: doc)
        Divider()
        Button(action: { findSimilar(doc: doc) }) {
            Label("Find Similar", systemImage: "arrow.triangle.2.circlepath")
        }
        Button(action: { runAgentAction(doc: doc) }) {
            Label("Run Agent Action", systemImage: "sparkles")
        }
        Divider()
        Button(action: { exportDocument(doc: doc) }) {
            Label("Reveal in Finder", systemImage: "folder")
        }
        Divider()
        Button(role: .destructive, action: { deleteDocument(doc: doc) }) {
            Label("Move to Trash", systemImage: "trash")
        }
    }

    @ViewBuilder
    private func statusSubmenu(doc: DocumentItem) -> some View {
        Menu("Status") {
            ForEach(DocumentStatus.allCases) { status in
                Button(action: {
                    Task { await setStatus(doc: doc, status: status.rawValue) }
                }) {
                    HStack {
                        Image(systemName: status.systemImage)
                        Text(status.label)
                        if doc.status == status.rawValue {
                            Image(systemName: "checkmark")
                        }
                    }
                }
            }
        }
    }

    @ViewBuilder
    private func tagsSubmenu(doc: DocumentItem) -> some View {
        Menu("Tags") {
            Button("Add Tag…") {
                addTag(doc: doc)
            }
            Divider()
            Text("Edit via properties in the inspector")
                .font(.caption)
                .foregroundColor(.secondary)
        }
    }

    // MARK: - Actions

    private func fetchDocs() async {
        isLoading = true
        defer { isLoading = false }
        do {
            self.documents = try await core.docsList()
            if let path = deepLinkPath, !path.isEmpty,
               let doc = documents.first(where: { $0.path == path }) {
                openDoc = doc
            }
        } catch {
            print("docsList failed: \(error)")
        }
    }

    private func setStatus(doc: DocumentItem, status: String) async {
        do {
            try await core.docSetStatus(path: doc.path, status: status)
            await fetchDocs()
        } catch {
            print("docSetStatus failed: \(error)")
        }
    }

    private func findSimilar(doc: DocumentItem) {
        Task {
            do {
                let similar = try await core.docsSimilar(path: doc.path)
                if similar.isEmpty {
                    print("No similar documents found")
                } else {
                    let titles = similar.map { $0.title }.joined(separator: ", ")
                    print("Similar documents: \(titles)")
                }
            } catch {
                print("findSimilar failed: \(error)")
            }
        }
    }

    private func runAgentAction(doc: DocumentItem) {
        agentTarget = doc
        agentQuery = "Summarize: \(doc.title)"
        showAgentSheet = true
    }

    private func exportDocument(doc: DocumentItem) {
        let url = URL(fileURLWithPath: doc.path)
        NSWorkspace.shared.activateFileViewerSelecting([url])
    }

    private func deleteDocument(doc: DocumentItem) {
        let url = URL(fileURLWithPath: doc.path)
        do {
            try FileManager.default.trashItem(at: url, resultingItemURL: nil)
            Task { await fetchDocs() }
        } catch {
            print("deleteDocument failed: \(error)")
        }
    }

    private func addTag(doc: DocumentItem) {
        let panel = NSSavePanel()
        panel.title = "Add Tag to \(doc.title)"
        panel.message = "Enter a tag name in the filename field, then Cancel — this is a placeholder for a future tag picker."
        panel.showsTagField = true
        panel.nameFieldStringValue = doc.title
        _ = panel.runModal()
    }

    // MARK: - Agent Sheet

    private var agentSheet: some View {
        VStack(spacing: 16) {
            HStack {
                Text("Agent Action")
                    .font(.headline)
                    .foregroundColor(SymairaTheme.goldPrimary)
                Spacer()
                Button(action: { showAgentSheet = false }) {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundColor(SymairaTheme.textMuted)
                }
                .buttonStyle(.plain)
            }

            if let target = agentTarget {
                Text("Document: \(target.title)")
                    .font(.subheadline)
                    .foregroundColor(SymairaTheme.textSecondary)
            }

            TextField("Ask about this document…", text: $agentQuery)
                .textFieldStyle(.roundedBorder)
                .onSubmit { executeAgent() }

            ScrollView {
                Text(agentResult.isEmpty ? "Press Enter to run…" : agentResult)
                    .font(.system(.body, design: .monospaced))
                    .foregroundColor(agentResult.isEmpty ? SymairaTheme.textMuted : SymairaTheme.textPrimary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .textSelection(.enabled)
            }
            .padding(10)
            .glassmorphicPanel(addCorners: false)

            HStack {
                Spacer()
                if isAgentRunning {
                    ProgressView()
                        .controlSize(.small)
                        .tint(SymairaTheme.goldPrimary)
                }
                Button("Run") { executeAgent() }
                    .buttonStyle(SymairaPrimaryButtonStyle())
                    .disabled(agentQuery.trimmingCharacters(in: .whitespaces).isEmpty || isAgentRunning)
            }
        }
        .padding(20)
        .frame(width: 500, height: 400)
        .background(SymairaTheme.bgDark)
    }

    private func executeAgent() {
        let q = agentQuery.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return }
        isAgentRunning = true
        agentResult = ""
        Task {
            do {
                let stream = core.ask(query: q)
                isAgentRunning = false
                for try await chunk in stream {
                    if chunk.type == .answer, let text = chunk.text {
                        agentResult += text
                    }
                }
            } catch {
                isAgentRunning = false
                agentResult += "\n\n**Error:** \(error.localizedDescription)"
            }
        }
    }
}

// MARK: - DocumentCard

struct DocumentCard: View {
    let doc: DocumentItem
    var isSelected: Bool = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .top) {
                Image(systemName: docTypeIcon)
                    .font(.system(size: 28))
                    .foregroundColor(SymairaTheme.goldPrimary)
                Spacer()
                if !doc.status.isEmpty {
                    statusBadge
                }
            }

            Text(doc.title)
                .font(.system(.body, weight: .semibold))
                .foregroundColor(SymairaTheme.textPrimary)
                .lineLimit(2)
                .frame(maxWidth: .infinity, alignment: .leading)

            if !doc.documentDate.isEmpty {
                HStack(spacing: 4) {
                    Image(systemName: "calendar")
                        .font(.caption2)
                        .foregroundColor(SymairaTheme.textSecondary)
                    Text(doc.documentDate)
                        .font(.caption)
                        .foregroundColor(SymairaTheme.textSecondary)
                }
            }

            if doc.asn > 0 {
                HStack(spacing: 4) {
                    Image(systemName: "number")
                        .font(.caption2)
                    Text("ASN \(doc.asn)")
                        .font(.caption.monospacedDigit())
                }
                .foregroundColor(SymairaTheme.goldSecondary)
            }

            HStack(spacing: 8) {
                if !doc.person.isEmpty {
                    HStack(spacing: 2) {
                        Image(systemName: "person")
                            .font(.caption2)
                        Text(doc.person)
                            .font(.caption)
                    }
                }
                if !doc.documentType.isEmpty {
                    HStack(spacing: 2) {
                        Image(systemName: "doc")
                            .font(.caption2)
                        Text(doc.documentType)
                            .font(.caption)
                    }
                }
            }
            .foregroundColor(SymairaTheme.textSecondary)

            if !doc.correspondent.isEmpty {
                Text(doc.correspondent)
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textSecondary)
                    .lineLimit(1)
            }

            if doc.confidence > 0 {
                confidenceBar
            }

            if !doc.dueDate.isEmpty {
                HStack(spacing: 4) {
                    Image(systemName: "clock")
                        .font(.caption2)
                        .foregroundColor(doc.dueDate < todayString ? .red : SymairaTheme.textSecondary)
                    Text("Due \(doc.dueDate)")
                        .font(.caption)
                        .foregroundColor(doc.dueDate < todayString ? .red : SymairaTheme.textSecondary)
                }
            }
        }
        .padding(12)
        .background(isSelected ? SymairaTheme.bgCardHover : SymairaTheme.bgCard)
        .cornerRadius(10)
        .overlay(
            RoundedRectangle(cornerRadius: 10)
                .stroke(isSelected ? SymairaTheme.goldPrimary.opacity(0.6) : SymairaTheme.borderGlass, lineWidth: isSelected ? 1.5 : 1)
        )
        .shadow(color: .black.opacity(0.35), radius: 8, y: 4)
    }

    private var statusBadge: some View {
        Text(statusLabel)
            .font(.system(.caption2, weight: .medium))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(statusColor.opacity(0.15))
            .foregroundColor(statusColor)
            .cornerRadius(4)
    }

    private var confidenceBar: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2)
                    .fill(Color.white.opacity(0.08))
                    .frame(height: 4)
                RoundedRectangle(cornerRadius: 2)
                    .fill(confidenceColor)
                    .frame(width: max(0, min(geo.size.width, geo.size.width * CGFloat(doc.confidence) / 100.0)), height: 4)
            }
        }
        .frame(height: 4)
        .overlay(
            Text("\(doc.confidence)%")
                .font(.system(.caption2, design: .monospaced))
                .foregroundColor(SymairaTheme.textMuted)
                .frame(maxWidth: .infinity, alignment: .trailing)
        )
    }

    private var statusLabel: String {
        DocumentStatus(rawValue: doc.status)?.label ?? doc.status
    }

    private var statusColor: Color {
        symairaStatusColor(DocumentStatus(rawValue: doc.status))
    }

    private var confidenceColor: Color {
        if doc.confidence >= 80 { return .green }
        if doc.confidence >= 50 { return .orange }
        return .red
    }

    private var docTypeIcon: String {
        switch doc.documentType.lowercased() {
        case "invoice": return "dollarsign.circle"
        case "receipt": return "receipt"
        case "contract": return "doc.plaintext"
        case "letter": return "envelope"
        case "tax": return "chart.bar.docpath"
        case "insurance": return "shield"
        default: return "doc.text"
        }
    }

    private var todayString: String {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM-dd"
        return f.string(from: Date())
    }
}
