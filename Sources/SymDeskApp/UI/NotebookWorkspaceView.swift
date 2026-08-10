import SwiftUI
import SymairaTheme
import SymDeskCore

/// The notebook workspace (issue #427): a list of notebooks on the left,
/// and — once one is selected — a three-pane surface (sources, grounded
/// chat, studio) on the right. Unlike `AIDockView` (a global, vault-wide
/// dock), every answer and citation here is bounded to the selected
/// notebook's sources (issue #425).
struct NotebookWorkspaceView: View {
    @EnvironmentObject var core: DeskCore
    var onOpenPath: (String) -> Void

    @State private var notebooks: [Notebook] = []
    @State private var selectedID: String?
    @State private var isLoading = false
    @State private var errorMessage: String?
    @State private var newTitle = ""
    @State private var isCreating = false

    var body: some View {
        HStack(spacing: 0) {
            notebookLane
                .frame(minWidth: 220, maxWidth: 280)

            Divider()

            if let selectedID {
                NotebookDetailView(
                    notebookID: selectedID,
                    onOpenPath: onOpenPath,
                    onDeleted: {
                        self.selectedID = nil
                        Task { await load() }
                    }
                )
            } else {
                emptyState
            }
        }
        .task { await load() }
    }

    private var notebookLane: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "books.vertical")
                    .foregroundStyle(SymairaTheme.goldPrimary)
                Text("Notebooks")
                    .symairaText(.subheading)
                    .foregroundColor(SymairaTheme.textPrimary)
                Spacer()
                Button(action: { Task { await load() } }) {
                    Image(systemName: "arrow.clockwise")
                }
                .buttonStyle(.plain)
                .foregroundColor(SymairaTheme.textSecondary)
                .disabled(isLoading)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)

            Divider()

            if let errorMessage {
                Text(errorMessage)
                    .symairaText(.caption)
                    .foregroundColor(.red)
                    .padding(8)
            }

            List(selection: $selectedID) {
                if notebooks.isEmpty && !isLoading {
                    Text("No notebooks yet.")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                }
                ForEach(notebooks) { notebook in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(notebook.title)
                            .symairaText(.body)
                            .foregroundColor(SymairaTheme.textPrimary)
                        Text("\(notebook.sources.count) source\(notebook.sources.count == 1 ? "" : "s")")
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textMuted)
                    }
                    .tag(notebook.id)
                }
            }
            .scrollContentBackground(.hidden)
            .listStyle(.sidebar)

            Divider()

            HStack(spacing: 8) {
                TextField("New notebook title…", text: $newTitle)
                    .textFieldStyle(.plain)
                    .onSubmit { Task { await createNotebook() } }
                Button(action: { Task { await createNotebook() } }) {
                    Image(systemName: "plus.circle.fill")
                        .foregroundColor(SymairaTheme.goldPrimary)
                }
                .buttonStyle(.plain)
                .disabled(newTitle.trimmingCharacters(in: .whitespaces).isEmpty || isCreating)
            }
            .padding(10)
        }
        .background(.clear)
    }

    private var emptyState: some View {
        VStack(spacing: 8) {
            Image(systemName: "books.vertical")
                .font(.system(size: 32))
                .foregroundColor(SymairaTheme.textMuted)
            Text("Select or create a notebook")
                .symairaText(.body)
                .foregroundColor(SymairaTheme.textSecondary)
            Text("A notebook is a bounded set of sources — questions and generated artifacts stay grounded in exactly what you add to it.")
                .symairaText(.caption)
                .foregroundColor(SymairaTheme.textMuted)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 320)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            notebooks = try await core.notebookList()
            errorMessage = nil
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func createNotebook() async {
        let title = newTitle.trimmingCharacters(in: .whitespaces)
        guard !title.isEmpty else { return }
        isCreating = true
        defer { isCreating = false }
        do {
            let created = try await core.notebookNew(title: title)
            newTitle = ""
            await load()
            selectedID = created.id
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}

/// The three-pane surface for one notebook: sources, grounded chat, studio.
private struct NotebookDetailView: View {
    @EnvironmentObject var core: DeskCore
    let notebookID: String
    var onOpenPath: (String) -> Void
    var onDeleted: () -> Void

    @State private var detail: NotebookDetail?
    @State private var isLoading = false
    @State private var errorMessage: String?
    @State private var newSourcePath = ""
    @State private var isAddingSource = false

    @State private var query = ""
    @State private var chatHistory: [ChatEntry] = []
    @State private var isThinking = false
    @State private var agentMode = false

    private static let kinds: [(id: String, label: String)] = [
        ("briefing", "Briefing"),
        ("study-guide", "Study Guide"),
        ("faq", "FAQ"),
        ("timeline", "Timeline"),
    ]
    @State private var selectedKind = kinds[0].id
    @State private var isGenerating = false
    @State private var generateDryRun = false
    @State private var artifact: NotebookArtifact?
    @State private var generateError: String?

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            HStack(spacing: 0) {
                sourcesPane
                    .frame(minWidth: 220, idealWidth: 250, maxWidth: 300)
                Divider()
                chatPane
                    .frame(minWidth: 320, maxWidth: .infinity)
                Divider()
                studioPane
                    .frame(minWidth: 260, idealWidth: 300, maxWidth: 360)
            }
        }
        .task(id: notebookID) {
            chatHistory = []
            artifact = nil
            generateError = nil
            await load()
        }
    }

    private var header: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text(detail?.title ?? notebookID)
                    .symairaText(.title).fontWeight(.semibold)
                    .foregroundColor(SymairaTheme.textPrimary)
                if let description = detail?.description, !description.isEmpty {
                    Text(description)
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textSecondary)
                }
            }
            Spacer()
            Button(role: .destructive, action: { Task { await deleteNotebook() } }) {
                Label("Delete", systemImage: "trash")
            }
            .buttonStyle(.plain)
            .foregroundColor(.red)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
    }

    // MARK: - Sources pane

    private var sourcesPane: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("Sources")
                .symairaText(.subheading)
                .foregroundColor(SymairaTheme.textPrimary)
                .padding(.horizontal, 12)
                .padding(.top, 12)

            if let errorMessage {
                Text(errorMessage)
                    .symairaText(.caption)
                    .foregroundColor(.red)
                    .padding(.horizontal, 12)
            }

            List {
                if let sources = detail?.sources, !sources.isEmpty {
                    ForEach(sources) { source in
                        sourceRow(source)
                    }
                } else {
                    Text("No sources yet — add a vault-relative path below.")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                }
            }
            .scrollContentBackground(.hidden)
            .listStyle(.sidebar)

            Divider()

            HStack(spacing: 6) {
                TextField("path/to/note.md", text: $newSourcePath)
                    .textFieldStyle(.plain)
                    .onSubmit { Task { await addSource() } }
                Button(action: { Task { await addSource() } }) {
                    Image(systemName: "plus.circle.fill")
                        .foregroundColor(SymairaTheme.goldPrimary)
                }
                .buttonStyle(.plain)
                .disabled(newSourcePath.trimmingCharacters(in: .whitespaces).isEmpty || isAddingSource)
            }
            .padding(10)
        }
    }

    private func sourceRow(_ source: NotebookSourceRef) -> some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text(source.title?.isEmpty == false ? source.title! : source.path)
                    .symairaText(.caption).bold()
                    .foregroundColor(source.missing == true ? SymairaTheme.textMuted : SymairaTheme.textPrimary)
                if source.missing == true {
                    Text("Missing")
                        .symairaText(.caption)
                        .foregroundColor(.orange)
                } else {
                    Text(source.path)
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
            }
            Spacer()
            if source.missing != true {
                Button(action: { onOpenPath(source.path) }) {
                    Image(systemName: "arrow.up.right.square")
                }
                .buttonStyle(.plain)
                .foregroundColor(SymairaTheme.goldSecondary)
                .help("Open note")
            }
            Button(action: { Task { await removeSource(source.path) } }) {
                Image(systemName: "minus.circle")
            }
            .buttonStyle(.plain)
            .foregroundColor(SymairaTheme.textMuted)
            .help("Remove from notebook (the file itself is untouched)")
        }
    }

    // MARK: - Chat pane

    private var chatPane: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "bubble.left.and.bubble.right")
                    .foregroundStyle(SymairaTheme.goldPrimary)
                Text("Grounded Chat")
                    .symairaText(.subheading)
                    .foregroundColor(SymairaTheme.textPrimary)
                Spacer()
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)

            Divider()

            ChatTranscriptView(chatHistory: chatHistory, isThinking: isThinking, onOpenFile: onOpenPath)

            Divider()

            HStack(spacing: 8) {
                Toggle(isOn: $agentMode) {
                    Text("Agent")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textSecondary)
                }
                .toggleStyle(.switch)
                .controlSize(.small)

                TextField("Ask about this notebook…", text: $query)
                    .textFieldStyle(.plain)
                    .onSubmit { submitQuery() }

                Button(action: submitQuery) {
                    Image(systemName: "paperplane.fill")
                        .foregroundColor(SymairaTheme.goldPrimary)
                }
                .buttonStyle(.plain)
                .disabled(query.trimmingCharacters(in: .whitespaces).isEmpty || isThinking)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
            .padding(12)
        }
    }

    private func submitQuery() {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return }

        query = ""
        chatHistory.append(.user(id: UUID(), text: q))
        isThinking = true

        Task {
            do {
                let stream = core.askScoped(query: q, notebook: notebookID, agent: agentMode)
                isThinking = false

                for try await event in stream {
                    appendChatEvent(event, to: &chatHistory)
                }
            } catch {
                isThinking = false
                chatHistory.append(.answer(id: UUID(), text: "\n\n**Error:** \(error.localizedDescription)"))
            }
        }
    }

    // MARK: - Studio pane

    private var studioPane: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "wand.and.stars")
                    .foregroundStyle(SymairaTheme.goldPrimary)
                Text("Studio")
                    .symairaText(.subheading)
                    .foregroundColor(SymairaTheme.textPrimary)
                Spacer()
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)

            Divider()

            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    Picker("Kind", selection: $selectedKind) {
                        ForEach(Self.kinds, id: \.id) { kind in
                            Text(kind.label).tag(kind.id)
                        }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()

                    Toggle("Preview only (don't save)", isOn: $generateDryRun)
                        .symairaText(.caption)
                        .toggleStyle(.checkbox)

                    Button(action: { Task { await generate() } }) {
                        HStack {
                            if isGenerating {
                                ProgressView().controlSize(.small)
                            }
                            Text(isGenerating ? "Generating…" : "Generate")
                        }
                        .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(isGenerating || (detail?.sources.isEmpty ?? true))

                    if let generateError {
                        Text(generateError)
                            .symairaText(.caption)
                            .foregroundColor(.red)
                    }

                    if let artifact {
                        Divider()
                        if !artifact.dryRun {
                            Button(action: { onOpenPath(artifact.path) }) {
                                Label("Open in Editor", systemImage: "arrow.up.right.square")
                            }
                            .buttonStyle(.plain)
                            .foregroundColor(SymairaTheme.goldSecondary)
                        }
                        if let warnings = artifact.citationWarnings, !warnings.isEmpty {
                            VStack(alignment: .leading, spacing: 2) {
                                Text("Unverified citations")
                                    .symairaText(.caption).bold()
                                    .foregroundColor(.orange)
                                ForEach(warnings, id: \.path) { warning in
                                    Text(warning.path)
                                        .symairaText(.caption)
                                        .foregroundColor(SymairaTheme.textMuted)
                                }
                            }
                        }
                        Text(artifact.content)
                            .symairaText(.callout)
                            .foregroundColor(SymairaTheme.textPrimary)
                            .textSelection(.enabled)
                    }
                }
                .padding(12)
            }
        }
    }

    // MARK: - Data

    private func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            detail = try await core.notebookShow(notebookID)
            errorMessage = nil
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func addSource() async {
        let path = newSourcePath.trimmingCharacters(in: .whitespaces)
        guard !path.isEmpty else { return }
        isAddingSource = true
        defer { isAddingSource = false }
        do {
            _ = try await core.notebookAddSource(notebookID, path: path)
            newSourcePath = ""
            await load()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func removeSource(_ path: String) async {
        do {
            _ = try await core.notebookRemoveSource(notebookID, path: path)
            await load()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func deleteNotebook() async {
        do {
            try await core.notebookDelete(notebookID)
            onDeleted()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func generate() async {
        isGenerating = true
        generateError = nil
        defer { isGenerating = false }
        do {
            artifact = try await core.notebookGenerate(notebookID, kind: selectedKind, dryRun: generateDryRun)
            if !generateDryRun {
                await load()
            }
        } catch {
            generateError = error.localizedDescription
        }
    }
}
