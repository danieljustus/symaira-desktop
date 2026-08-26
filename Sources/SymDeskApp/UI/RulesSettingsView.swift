import SwiftUI
import SymairaProviderKit
import SymairaTheme
import SymDeskCore
import SymairaIngestContract

struct RulesSettingsView: View {
	@EnvironmentObject private var core: DeskCore
	@EnvironmentObject private var watcher: EventWatcher
    @StateObject private var viewModel: ClassificationRulesViewModel
    @State private var editingRule: ClassificationRule?
    @State private var editingMail: MailAccount?
    @State private var showingRuleEditor = false
    @State private var showingMailEditor = false
    @State private var pendingDeleteRule: ClassificationRule?
    @State private var pendingDeleteMail: MailAccount?
    @State private var showingChangeVaultConfirmation = false
    @State private var consumeFolderStatus: DeskCore.ConsumeFolderStatus?
    @State private var consumeFolderError: String?
    @State private var editingConsumeFolderPath = false
    @State private var editedConsumeFolderPath = ""

    @State private var aiConfig: DeskCore.AIConfig?
    @State private var aiConfigError: String?
    @State private var aiProvider: String = "ollama"
    @State private var aiOllamaURL: String = ""
    @State private var aiModel: String = ""
    @State private var aiAPIKey: String = ""
    @State private var aiMaxTokens: String = ""
    @State private var aiAvailableModels: [String] = []
    @State private var aiTestResult: DeskCore.AIConnectionTestResult?
    @State private var aiIsTesting = false
    @State private var aiIsSaving = false
    @State private var showingPaperlessImport = false

    init() {
        _viewModel = StateObject(wrappedValue: ClassificationRulesViewModel(client: DeskCore.shared))
    }

    init(client: any ClassificationRulesClient) {
        _viewModel = StateObject(wrappedValue: ClassificationRulesViewModel(client: client))
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                header
				connectionCard
				displayCard
				if core.isRemote {
					messageCard(
						title: "Processing is managed by SymDesk Server",
						message: "Upload and queue status work from this Mac. Classification and mail-ingest settings remain server-side for now.",
						systemImage: "server.rack"
					) { EmptyView() }
				}
				if !core.isRemote {
                if let error = viewModel.lastError, viewModel.lastErrorKind == .availability {
                    let title = "Rules unavailable"
                    messageCard(title: title, message: error, systemImage: "exclamationmark.triangle") {
                        Button("Retry") { Task { await viewModel.load(); await viewModel.loadMail() } }
                            .buttonStyle(.bordered)
                            .controlSize(.small)
                    }
                }
                if let action = viewModel.lastActionMessage {
                    Label(action, systemImage: "checkmark.circle")
                        .foregroundStyle(SymairaTheme.goldPrimary)
                        .symairaText(.callout)
                }
                classificationRulesCard
                dryRunCard
                mailRulesCard
                consumeFolderCard
                paperlessImportCard
                aiSettingsCard
				}
            }
            .frame(maxWidth: 960, alignment: .leading)
            .padding(32)
        }
        .background(SymairaTheme.bgDark)
        .navigationTitle("Rules & Settings")
        .task {
			guard !core.isRemote else { return }
            await viewModel.load()
            await viewModel.loadMail()
            await loadConsumeFolderStatus()
        }
        .sheet(isPresented: $showingRuleEditor) {
            RuleEditorView(existing: editingRule) { pattern, kind, value in
                Task {
                    if await viewModel.saveRule(id: editingRule?.id, pattern: pattern, kind: kind, value: value) {
                        showingRuleEditor = false
                        editingRule = nil
                    }
                }
            }
        }
        .sheet(isPresented: $showingMailEditor) {
            MailEditorView(existing: editingMail) { account in
                Task {
                    if await viewModel.saveMail(id: editingMail?.stableID, account: account) {
                        showingMailEditor = false
                        editingMail = nil
                    }
                }
            }
        }
        .sheet(isPresented: $showingPaperlessImport) {
            PaperlessImportView()
        }
        .confirmationDialog("Delete Classification Rule?", isPresented: Binding(
            get: { pendingDeleteRule != nil },
            set: { if !$0 { pendingDeleteRule = nil } }
        ), titleVisibility: .visible) {
            Button("Delete Rule", role: .destructive) {
                guard let rule = pendingDeleteRule else { return }
                pendingDeleteRule = nil
                Task { _ = await viewModel.deleteRule(id: rule.id) }
            }
            Button("Cancel", role: .cancel) { pendingDeleteRule = nil }
        }
        .confirmationDialog("Delete Mail Account?", isPresented: Binding(
            get: { pendingDeleteMail != nil },
            set: { if !$0 { pendingDeleteMail = nil } }
        ), titleVisibility: .visible) {
            Button("Delete Mail Account", role: .destructive) {
                guard let account = pendingDeleteMail else { return }
                pendingDeleteMail = nil
                Task { _ = await viewModel.deleteMail(id: account.stableID) }
            }
            Button("Cancel", role: .cancel) { pendingDeleteMail = nil }
        }
        .confirmationDialog("Change Vault?", isPresented: $showingChangeVaultConfirmation, titleVisibility: .visible) {
            Button("Change Vault", role: .destructive) {
                VaultConfig.reset()
                core.vaultPath = nil
                NotificationCenter.default.post(name: .vaultReset, object: nil)
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This clears the current vault association and returns you to setup. Your files are not deleted.")
        }
    }

    private var header: some View {
        HStack(alignment: .top) {
            VStack(alignment: .leading, spacing: 6) {
                Text("Rules & Settings")
                    .symairaText(.display).bold()
                    .foregroundStyle(SymairaTheme.textPrimary)
                Text("Manage classification and mail-ingest behavior through symdesk's versioned contract.")
                    .foregroundStyle(SymairaTheme.textSecondary)
            }
            Spacer()
            Button {
                editingRule = nil
                showingRuleEditor = true
            } label: {
                Label("New Classification Rule", systemImage: "plus")
            }
            .buttonStyle(.borderedProminent)
            .tint(SymairaTheme.goldPrimary)
        }
    }

	private var connectionCard: some View {
	    settingsCard(title: core.isRemote ? "Self-hosted server" : "Local vault", systemImage: core.isRemote ? "server.rack" : "internaldrive") {
	        HStack(spacing: 14) {
	            VStack(alignment: .leading, spacing: 5) {
	                Text(core.isRemote ? (core.serverURL?.absoluteString ?? "Connected") : (core.vaultPath ?? "Not configured"))
	                    .symairaText(.subheading)
	                    .foregroundStyle(SymairaTheme.textPrimary)
	                Text(core.isRemote ? "Vault, originals, index and OCR queue live on this server." : "The app and CLI read this vault directly on your Mac.")
	                    .symairaText(.callout)
	                    .foregroundStyle(SymairaTheme.textSecondary)
	            }
	            Spacer()
	            if core.isRemote {
	                Button("Disconnect", role: .destructive) {
	                    VaultConfig.reset()
	                    core.disconnectServer()
	                    NSApplication.shared.terminate(nil)
	                }
	                .buttonStyle(.bordered)
	            } else if !core.isDemoMode {
	                Button("Reveal in Finder") {
	                    if let path = core.vaultPath {
	                        NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: path)
	                    }
	                }
	                .buttonStyle(.bordered)
	                Button("Change Vault…") {
	                    showingChangeVaultConfirmation = true
	                }
	                .buttonStyle(.bordered)
	            }
	        }
	        if !core.isRemote && !core.isDemoMode {
	            Divider().overlay(SymairaTheme.borderGlass)
	            Toggle(isOn: finderFavoritesBinding) {
	                VStack(alignment: .leading, spacing: 2) {
	                    Text("Show vault in Finder's Favorites sidebar")
	                        .symairaText(.body)
	                        .foregroundStyle(SymairaTheme.textPrimary)
	                    Text("Adds the vault folder to Finder's sidebar for one-click access. Removing it from Finder's sidebar is respected and not undone automatically.")
	                        .symairaText(.caption)
	                        .foregroundStyle(SymairaTheme.textSecondary)
	                }
	            }
	            .toggleStyle(.switch)
	            .tint(SymairaTheme.goldPrimary)
	        }
	    }
	}

	private var finderFavoritesBinding: Binding<Bool> {
	    Binding(
	        get: { VaultConfig.finderFavoritesEnabled },
	        set: { VaultConfig.finderFavoritesEnabled = $0 }
	    )
	}

	@AppStorage("showDocumentThumbnails") private var showThumbnails = true

	private var displayCard: some View {
	    settingsCard(title: "Display", systemImage: "eye") {
	        Toggle(isOn: $showThumbnails) {
	            VStack(alignment: .leading, spacing: 2) {
	                Text("Show content preview thumbnails")
	                    .symairaText(.body)
	                    .foregroundStyle(SymairaTheme.textPrimary)
	                Text("When enabled, document and note cards show a text preview of the first content instead of a generic SF Symbol icon.")
	                    .symairaText(.caption)
	                    .foregroundStyle(SymairaTheme.textSecondary)
	            }
	        }
	        .toggleStyle(.switch)
	        .tint(SymairaTheme.goldPrimary)
	    }
	}

    private var classificationRulesCard: some View {
        settingsCard(title: "Classification rules", systemImage: "line.3.horizontal.decrease.circle") {
            if viewModel.isLoading && viewModel.rules.isEmpty {
                ProgressView("Loading rules…")
            } else if viewModel.rules.isEmpty {
                Text("No classification rules configured.")
                    .foregroundStyle(SymairaTheme.textSecondary)
            } else {
                VStack(spacing: 0) {
                    ForEach(viewModel.rules) { rule in
                        HStack(alignment: .top, spacing: 12) {
                            Image(systemName: "line.3.horizontal.decrease.circle")
                                .foregroundStyle(SymairaTheme.goldPrimary)
                            VStack(alignment: .leading, spacing: 4) {
                                Text(rule.pattern).symairaText(.subheading)
                                Text("\(rule.kind) → \(rule.value)")
                                    .symairaText(.callout)
                                    .foregroundStyle(SymairaTheme.textSecondary)
                            }
                            Spacer()
                            Button("Edit") { editingRule = rule; showingRuleEditor = true }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                            Button { pendingDeleteRule = rule } label: { Image(systemName: "trash") }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                        }
                        .padding(.vertical, 10)
                        if rule.id != viewModel.rules.last?.id { Divider().overlay(SymairaTheme.borderGlass) }
                    }
                }
            }
        }
    }

    private var dryRunCard: some View {
        settingsCard(title: "Existing-document dry-run", systemImage: "doc.text.magnifyingglass") {
            Text("Scan indexed Markdown notes without writing anything. Results include only safe metadata and matching rule IDs.")
                .foregroundStyle(SymairaTheme.textSecondary)
            HStack {
                TextField("Pattern", text: $viewModel.dryRunPattern)
                TextField("Kind", text: $viewModel.dryRunKind)
                    .frame(width: 150)
                TextField("Value", text: $viewModel.dryRunValue)
            }
            Button {
                Task { _ = await viewModel.runDryRun() }
            } label: {
                Label("Run dry-run", systemImage: "play.fill")
            }
            .buttonStyle(.borderedProminent)
            .tint(SymairaTheme.goldPrimary)
            .disabled(viewModel.isPerformingOperation)

            if let result = viewModel.dryRunResult {
                Text("\(result.matchedDocuments) of \(result.totalDocuments) documents match")
                    .symairaText(.subheading)
                ForEach(result.matches) { match in
                    Label("\(match.title) — \(match.notePath)", systemImage: "checkmark.circle")
                        .symairaText(.callout)
                        .foregroundStyle(SymairaTheme.textSecondary)
                }
                ForEach(result.skipped) { skipped in
                    Label("Skipped: \(skipped.notePath) — \(skipped.reason)", systemImage: "exclamationmark.triangle")
                        .symairaText(.callout)
                        .foregroundStyle(SymairaTheme.goldSecondary)
                }
            }

            if let error = viewModel.lastError, viewModel.lastErrorKind == .validation {
                Text(error)
                    .symairaText(.callout)
                    .foregroundStyle(.red)
            }
        }
    }

    private var mailRulesCard: some View {
        settingsCard(title: "Mail-ingest rules", systemImage: "envelope.badge") {
            if let error = viewModel.mailError {
                messageCard(title: "Mail configuration unavailable", message: error, systemImage: "hourglass") {
                    Button("Retry") { Task { await viewModel.loadMail() } }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                }
            } else {
                HStack {
                    Text("Changes apply after restarting the mail watcher.")
                        .symairaText(.callout)
                        .foregroundStyle(SymairaTheme.textSecondary)
                    Spacer()
                    Button("New Mail Account") { editingMail = nil; showingMailEditor = true }
                        .buttonStyle(.borderedProminent)
                        .tint(SymairaTheme.goldPrimary)
                }
                if viewModel.mailAccounts.isEmpty {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Mail import isn't set up yet.")
                            .symairaText(.callout).fontWeight(.medium)
                            .foregroundStyle(SymairaTheme.textPrimary)
                        Text("Click \"New Mail Account\" above to configure an IMAP account for automated email ingestion.")
                            .symairaText(.caption)
                            .foregroundStyle(SymairaTheme.textSecondary)
                    }
                    .padding(.vertical, 4)
                } else {
                    ForEach(viewModel.mailAccounts, id: \.stableID) { account in
                        HStack(alignment: .top, spacing: 12) {
                            Image(systemName: "envelope")
                                .foregroundStyle(SymairaTheme.goldPrimary)
                            VStack(alignment: .leading, spacing: 4) {
                                Text(account.username + "@" + account.host).symairaText(.subheading)
                                Text("\(account.action) · \(account.folder) · password: \(account.passwordSecretKind ?? "unknown")")
                                    .symairaText(.callout)
                                    .foregroundStyle(SymairaTheme.textSecondary)
                                if !account.from.isEmpty || !account.subject.isEmpty {
                                    Text("Filters: \((account.from + account.subject).joined(separator: ", "))")
                                        .symairaText(.caption)
                                        .foregroundStyle(SymairaTheme.textMuted)
                                }
                            }
                            Spacer()
                            Button("Edit") { editingMail = account; showingMailEditor = true }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                            Button { pendingDeleteMail = account } label: { Image(systemName: "trash") }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                        }
                        .padding(.vertical, 8)
                    }
                }
            }
        }
    }

    /// Shared settings card, built on the appkit form scaffold (issue #352):
    /// section label header + glass card body.
    private func settingsCard<Content: View>(title: String, systemImage: String, @ViewBuilder content: () -> Content) -> some View {
        SymairaFormSection(title) {
            content()
        }
    }

    /// Card offering the guided Paperless-ngx export import (issue #307).
    private var paperlessImportCard: some View {
        settingsCard(title: "Paperless-ngx import", systemImage: "doc.badge.arrow.up") {
            VStack(alignment: .leading, spacing: 10) {
                Text("Migrate documents from a Paperless-ngx export into this vault. Notes are created with contract frontmatter and the originals are archived under archive/paperless/.")
                    .symairaText(.callout)
                    .foregroundStyle(SymairaTheme.textSecondary)
                Button {
                    showingPaperlessImport = true
                } label: {
                    Label("Import from Paperless…", systemImage: "arrow.down.doc")
                }
                .buttonStyle(.borderedProminent)
                .tint(SymairaTheme.goldPrimary)
                .controlSize(.small)
            }
        }
    }

    /// Card that shows and controls the consume (watched inbox) folder.
    private var consumeFolderCard: some View {
        settingsCard(title: "Consume folder", systemImage: "tray.and.arrow.down") {
            if let error = consumeFolderError {
                messageCard(title: "Could not load consume folder status", message: error, systemImage: "exclamationmark.triangle") {
                    Button("Retry") {
                        Task { await loadConsumeFolderStatus() }
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                }
            } else if let status = consumeFolderStatus {
                VStack(alignment: .leading, spacing: 12) {
                    // Folder path display / edit
                    if editingConsumeFolderPath {
                        VStack(alignment: .leading, spacing: 6) {
                            TextField("Folder path", text: $editedConsumeFolderPath)
                                .textFieldStyle(.symaira)
                            HStack {
                                Button("Save") {
                                    Task {
                                        do {
                                            try await core.setConsumeFolderPath(editedConsumeFolderPath)
                                            editingConsumeFolderPath = false
                                            await loadConsumeFolderStatus()
                                        } catch {
                                            consumeFolderError = error.localizedDescription
                                        }
                                    }
                                }
                                .buttonStyle(.borderedProminent)
                                .controlSize(.small)
                                .tint(SymairaTheme.goldPrimary)
                                Button("Cancel") {
                                    editingConsumeFolderPath = false
                                }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                            }
                        }
                    } else {
                        HStack {
                            VStack(alignment: .leading, spacing: 4) {
                                Text(status.inboxPath)
                                    .symairaText(.subheading)
                                    .foregroundStyle(SymairaTheme.textPrimary)
                                Text(status.configuredPath.isEmpty
                                     ? "Default path (not explicitly configured)"
                                     : "Configured in symdesk config")
                                    .symairaText(.caption)
                                    .foregroundStyle(SymairaTheme.textMuted)
                            }
                            Spacer()
                            Button("Change") {
                                editedConsumeFolderPath = status.inboxPath
                                editingConsumeFolderPath = true
                            }
                            .buttonStyle(.bordered)
                            .controlSize(.small)
                        }
                    }

                    Divider().overlay(SymairaTheme.borderGlass)

                    // Watch status and toggle
                    HStack {
                        Label(watcher.isWatching ? "Running" : "Stopped",
                              systemImage: watcher.isWatching ? "play.fill" : "stop.fill")
                            .foregroundStyle(watcher.isWatching ? .green : SymairaTheme.textSecondary)
                            .symairaText(.callout)
                        Spacer()
                        if watcher.isWatching {
                            Button("Stop") {
                                watcher.stop()
                            }
                            .buttonStyle(.bordered)
                            .controlSize(.small)
                        } else {
                            Button("Start") {
                                if let tool = core.tool {
                                    watcher.start(tool: tool, vaultPath: core.vaultPath)
                                }
                            }
                            .buttonStyle(.borderedProminent)
                            .controlSize(.small)
                            .tint(SymairaTheme.goldPrimary)
                            .disabled(core.tool == nil)
                        }
                    }

                    if !watcher.allEvents.isEmpty {
                        Divider().overlay(SymairaTheme.borderGlass)

                        // Recent activity
                        Text("Recent activity")
                            .symairaText(.subheading)
                            .foregroundStyle(SymairaTheme.textPrimary)

                        ForEach(watcher.recentActivity.prefix(5)) { event in
                            HStack(spacing: 8) {
                                Image(systemName: iconForEvent(event.event))
                                    .foregroundStyle(SymairaTheme.goldSecondary)
                                    .symairaText(.caption)
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(event.event.replacingOccurrences(of: "_", with: " ").capitalized)
                                        .symairaText(.caption).fontWeight(.medium)
                                        .foregroundStyle(SymairaTheme.textPrimary)
                                    Text(URL(fileURLWithPath: event.path).lastPathComponent)
                                        .symairaText(.caption)
                                        .foregroundStyle(SymairaTheme.textMuted)
                                        .lineLimit(1)
                                }
                                Spacer()
                                Text(formattedTimestamp(event.ts))
                                    .symairaText(.caption)
                                    .foregroundStyle(SymairaTheme.textMuted)
                            }
                            .padding(.vertical, 2)
                        }
                    }
                }
            } else {
                ProgressView("Loading consume folder status…")
                    .task { await loadConsumeFolderStatus() }
            }
        }
    }

    private func loadConsumeFolderStatus() async {
        consumeFolderError = nil
        do {
            consumeFolderStatus = try await core.getConsumeFolderStatus()
        } catch {
            consumeFolderError = error.localizedDescription
        }
    }

    /// Card for configuring the AI provider (Ollama / Anthropic).
    private var aiSettingsCard: some View {
        settingsCard(title: "AI", systemImage: "sparkles") {
            if let error = aiConfigError {
                messageCard(title: "Could not load AI configuration", message: error, systemImage: "exclamationmark.triangle") {
                    Button("Retry") {
                        Task { await loadAIConfig() }
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                }
            } else if aiConfig == nil {
                ProgressView("Loading AI configuration…")
                    .task { await loadAIConfig() }
            } else {
                VStack(alignment: .leading, spacing: 12) {
                    // AI off is a desk-level state, not a provider: the
                    // shared picker (appkit) manages only real providers,
                    // filtered to the ones the Go core implements.
                    Toggle("Enable AI", isOn: Binding(
                        get: { aiProvider != "none" },
                        set: { aiProvider = $0 ? "ollama" : "none" }
                    ))
                    .toggleStyle(.switch)
                    .symairaText(.body)

                    if aiProvider != "none" {
                        if let catalog = aiProviderCatalog {
                            SymairaProviderPicker(selection: $aiProvider, catalog: catalog, title: "Provider")
                                .pickerStyle(.segmented)
                        } else {
                            Text("Provider catalog unavailable")
                                .symairaText(.caption)
                                .foregroundStyle(SymairaTheme.textSecondary)
                        }
                    }

                    if aiProvider == "ollama" {
                        TextField("Ollama endpoint URL (e.g. http://localhost:11434)", text: $aiOllamaURL)
                            .textFieldStyle(.symaira)

                        if aiAvailableModels.isEmpty {
                            TextField("Model", text: $aiModel)
                                .textFieldStyle(.symaira)
                        } else {
                            Picker("Model", selection: $aiModel) {
                                ForEach(aiAvailableModels, id: \.self) { name in
                                    Text(name).tag(name)
                                }
                            }
                        }
                    } else if aiProvider == "anthropic" {
                        SecureField("API key or symvault reference (op://...)", text: $aiAPIKey)
                            .textFieldStyle(.symaira)
                        TextField("Model", text: $aiModel)
                            .textFieldStyle(.symaira)
                    }

                    TextField("Max tokens", text: $aiMaxTokens)
                        .textFieldStyle(.symaira)
                        .frame(maxWidth: 160)

                    HStack {
                        Button("Test connection") {
                            Task { await testAIConnection() }
                        }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                        .disabled(aiIsTesting)

                        Button(aiIsSaving ? "Saving…" : "Save") {
                            Task { await saveAIConfig() }
                        }
                        .buttonStyle(.borderedProminent)
                        .controlSize(.small)
                        .tint(SymairaTheme.goldPrimary)
                        .disabled(aiIsSaving)

                        Spacer()
                    }

                    if aiIsTesting {
                        ProgressView("Testing connection…")
                    } else if let result = aiTestResult {
                        if result.ok {
                            Label(
                                result.models?.isEmpty == false
                                    ? "Connected. Models: \(result.models!.joined(separator: ", "))"
                                    : "Connected.",
                                systemImage: "checkmark.circle"
                            )
                            .foregroundStyle(.green)
                            .symairaText(.caption)
                        } else {
                            Label(result.error ?? "Connection failed.", systemImage: "exclamationmark.triangle")
                                .foregroundStyle(.orange)
                                .symairaText(.caption)
                        }
                    }
                }
            }
        }
    }

    private var aiProviderCatalog: SymairaProviderCatalog? {
        let all = SymairaProviderCatalog.bundled
        let supported = all.providers.filter { ["ollama", "anthropic"].contains($0.id) }
        return try? SymairaProviderCatalog(schemaVersion: 1, providers: supported)
    }

    private func loadAIConfig() async {
        aiConfigError = nil
        do {
            let config = try await core.getAIConfig()
            aiConfig = config
            aiProvider = config.provider
            aiOllamaURL = config.ollamaURL
            aiModel = config.model
            aiMaxTokens = String(config.maxTokens)
        } catch {
            aiConfigError = error.localizedDescription
        }
    }

    private func testAIConnection() async {
        aiIsTesting = true
        aiTestResult = nil
        defer { aiIsTesting = false }
        do {
            // Save first so the test reflects the values currently on screen.
            try await core.setAIConfig(
                provider: aiProvider,
                ollamaURL: aiOllamaURL,
                model: aiModel.isEmpty ? nil : aiModel,
                apiKey: aiProvider == "anthropic" ? aiAPIKey : nil,
                maxTokens: Int(aiMaxTokens)
            )
            let result = try await core.testAIConnection()
            aiTestResult = result
            if let models = result.models, !models.isEmpty {
                aiAvailableModels = models
                if !models.contains(aiModel), let first = models.first {
                    aiModel = first
                }
            }
        } catch {
            aiTestResult = DeskCore.AIConnectionTestResult(provider: aiProvider, ok: false, error: error.localizedDescription, models: nil)
        }
    }

    private func saveAIConfig() async {
        aiIsSaving = true
        defer { aiIsSaving = false }
        do {
            try await core.setAIConfig(
                provider: aiProvider,
                ollamaURL: aiOllamaURL,
                model: aiModel.isEmpty ? nil : aiModel,
                apiKey: aiProvider == "anthropic" ? aiAPIKey : nil,
                maxTokens: Int(aiMaxTokens)
            )
            await loadAIConfig()
        } catch {
            aiConfigError = error.localizedDescription
        }
    }

    private func iconForEvent(_ event: String) -> String {
        switch event {
        case "file_added", "index_updated": return "doc.badge.plus"
        case "file_changed": return "doc.badge.gearshape"
        case "file_removed": return "trash"
        default: return "circle.fill"
        }
    }

    private func formattedTimestamp(_ ts: Int64) -> String {
        let date = Date(timeIntervalSince1970: TimeInterval(ts))
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .abbreviated
        return formatter.localizedString(for: date, relativeTo: Date())
    }

    private func messageCard<Content: View>(title: String, message: String, systemImage: String, @ViewBuilder action: () -> Content) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: systemImage).foregroundStyle(SymairaTheme.goldSecondary)
            VStack(alignment: .leading, spacing: 6) {
                Text(title).symairaText(.subheading).foregroundStyle(SymairaTheme.textPrimary)
                Text(message).foregroundStyle(SymairaTheme.textSecondary)
                action()
            }
            Spacer()
        }
        .padding(14)
        .background(SymairaTheme.goldPrimary.opacity(0.08))
        .clipShape(RoundedRectangle(cornerRadius: 10))
    }
}

private struct RuleEditorView: View {
    @Environment(\.dismiss) private var dismiss
    private let existing: ClassificationRule?
    private let onSave: (String, String, String) -> Void
    @State private var pattern: String
    @State private var kind: String
    @State private var value: String

    init(existing: ClassificationRule?, onSave: @escaping (String, String, String) -> Void) {
        self.existing = existing
        self.onSave = onSave
        _pattern = State(initialValue: existing?.pattern ?? "")
        _kind = State(initialValue: existing?.kind ?? "category")
        _value = State(initialValue: existing?.value ?? "")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            Text(existing == nil ? "New Classification Rule" : "Edit Classification Rule")
                .symairaText(.title).bold()
            Form {
                TextField("Pattern", text: $pattern)
                TextField("Kind: category, tag, correspondent, document_type", text: $kind)
                TextField("Value", text: $value)
            }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                Button(existing == nil ? "Add Rule" : "Save Rule") { onSave(pattern, kind, value) }
                    .buttonStyle(.borderedProminent)
                    .tint(SymairaTheme.goldPrimary)
                    .disabled(pattern.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || kind.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .padding(24)
        .frame(width: 520, height: 300)
        .background(SymairaTheme.bgDark)
    }
}

private struct MailEditorView: View {
    @Environment(\.dismiss) private var dismiss
    private let existing: MailAccount?
    private let onSave: (MailAccount) -> Void
    @State private var host: String
    @State private var port: String
    @State private var username: String
    @State private var passwordSecret: String
    @State private var folder: String
    @State private var from: String
    @State private var subject: String
    @State private var action: String
    @State private var moveTo: String
    @State private var hasAttachment: Bool
    @State private var archiveMail: Bool

    init(existing: MailAccount?, onSave: @escaping (MailAccount) -> Void) {
        self.existing = existing
        self.onSave = onSave
        _host = State(initialValue: existing?.host ?? "")
        _port = State(initialValue: existing.map { String($0.port) } ?? "993")
        _username = State(initialValue: existing?.username ?? "")
        _passwordSecret = State(initialValue: existing?.passwordSecret == "<redacted>" ? "" : (existing?.passwordSecret ?? ""))
        _folder = State(initialValue: existing?.folder ?? "INBOX")
        _from = State(initialValue: existing?.from.joined(separator: ", ") ?? "")
        _subject = State(initialValue: existing?.subject.joined(separator: ", ") ?? "")
        _action = State(initialValue: existing?.action ?? "mark_seen")
        _moveTo = State(initialValue: existing?.moveTo ?? "")
        _hasAttachment = State(initialValue: existing?.hasAttachment ?? false)
        _archiveMail = State(initialValue: existing?.archiveMail ?? false)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(existing == nil ? "New Mail Account" : "Edit Mail Account")
                .symairaText(.title).bold()
            Form {
                TextField("IMAP host", text: $host)
                TextField("Port", text: $port)
                TextField("Username", text: $username)
                SecureField(existing == nil ? "Password secret reference" : "New password secret reference (leave empty to preserve)", text: $passwordSecret)
                TextField("Folder", text: $folder)
                TextField("From filters, comma-separated", text: $from)
                TextField("Subject filters, comma-separated", text: $subject)
                TextField("Action: mark_seen or move", text: $action)
                TextField("Move to", text: $moveTo)
                Toggle("Require attachment", isOn: $hasAttachment)
                Toggle("Archive .eml message", isOn: $archiveMail)
            }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                Button(existing == nil ? "Create Account" : "Save Account") {
                    let account = MailAccount(
                        id: existing?.id,
                        host: host.trimmingCharacters(in: .whitespacesAndNewlines),
                        port: Int(port) ?? 993,
                        username: username.trimmingCharacters(in: .whitespacesAndNewlines),
                        passwordSecret: passwordSecret.isEmpty ? nil : passwordSecret,
                        folder: folder,
                        from: split(from),
                        subject: split(subject),
                        hasAttachment: hasAttachment,
                        action: action,
                        moveTo: moveTo,
                        archiveMail: archiveMail
                    )
                    onSave(account)
                }
                .buttonStyle(.borderedProminent)
                .tint(SymairaTheme.goldPrimary)
                .disabled(host.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || username.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .padding(24)
        .frame(width: 600, height: 560)
        .background(SymairaTheme.bgDark)
    }

    private func split(_ value: String) -> [String] {
        value.split(separator: ",").map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
    }
}
