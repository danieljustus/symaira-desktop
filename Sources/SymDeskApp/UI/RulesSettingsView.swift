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
    @State private var aiModelNotice: String?
    @State private var aiMaxTokens: String = ""
    @State private var aiAvailableModels: [String] = []
    @State private var aiTestResult: DeskCore.AIConnectionTestResult?
    @State private var aiIsTesting = false
    @State private var aiIsSaving = false
    private let aiCredentialStore = SymairaProviderCredentialStore()
    @State private var showingPaperlessImport = false

    init() {
        _viewModel = StateObject(wrappedValue: ClassificationRulesViewModel(client: DeskCore.shared))
    }

    init(client: any ClassificationRulesClient) {
        _viewModel = StateObject(wrappedValue: ClassificationRulesViewModel(client: client))
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: SymairaSpacing.xLarge) {
                header
				connectionCard
				displayCard
				if core.isRemote {
					messageCard(
						title: "Processing is managed by SymDesk Server",
						message: "Upload and queue status work from this Mac. Classification and mail-ingest settings remain server-side for now.",
						icon: .server
					) { EmptyView() }
				}
				if !core.isRemote {
                if let error = viewModel.lastError, viewModel.lastErrorKind == .availability {
                    let title = "Rules unavailable"
                    messageCard(title: title, message: error, icon: .warning) {
                        Button("Retry") { Task { await viewModel.load(); await viewModel.loadMail() } }
                            .symairaButtonStyle(.secondary)
                    }
                }
                if let action = viewModel.lastActionMessage {
                    SymairaNotice(message: action, tone: .positive)
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
            .padding(SymairaSpacing.section)
        }
        .background(SymairaTokens.canvas)
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
            VStack(alignment: .leading, spacing: SymairaSpacing.xSmall) {
                Text("Rules & Settings")
                    .symairaText(.display)
                Text("Manage classification and mail-ingest behavior through symdesk's versioned contract.")
                    .symairaText(.secondary)
            }
            Spacer()
            Button {
                editingRule = nil
                showingRuleEditor = true
            } label: {
                Label("New Classification Rule", symairaIcon: .add)
            }
            .symairaButtonStyle(.primary)
        }
    }

	private var connectionCard: some View {
	    settingsCard(title: core.isRemote ? "Self-hosted server" : "Local vault", icon: core.isRemote ? .server : .storage) {
	        HStack(spacing: 14) {
	            VStack(alignment: .leading, spacing: 5) {
            Text(core.isRemote ? (core.serverURL?.absoluteString ?? "Connected") : (core.vaultPath ?? "Not configured"))
	                    .symairaText(.subheading)
	                Text(core.isRemote ? "Vault, originals, index and OCR queue live on this server." : "The app and CLI read this vault directly on your Mac.")
	                    .symairaText(.secondary)
	            }
	            Spacer()
	            if core.isRemote {
	                Button("Disconnect", role: .destructive) {
	                    VaultConfig.reset()
	                    core.disconnectServer()
	                    NSApplication.shared.terminate(nil)
	                }
	                .symairaButtonStyle(.secondary)
	            } else if !core.isDemoMode {
	                Button("Reveal in Finder") {
	                    if let path = core.vaultPath {
	                        NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: path)
	                    }
	                }
	                .symairaButtonStyle(.secondary)
	                Button("Change Vault…") {
	                    showingChangeVaultConfirmation = true
	                }
	                .symairaButtonStyle(.secondary)
	            }
	        }
	        if !core.isRemote && !core.isDemoMode {
	            SymairaFormDivider()
	            SymairaFormRow(
	                "Show vault in Finder's Favorites sidebar",
	                description: "Adds the vault folder to Finder's sidebar for one-click access. Removing it from Finder's sidebar is respected and not undone automatically."
	            ) {
	                Toggle(isOn: finderFavoritesBinding) { EmptyView() }
	                    .toggleStyle(.symairaSwitch)
	                    .accessibilityLabel("Show vault in Finder's Favorites sidebar")
	                    .alignmentGuide(.firstTextBaseline) { $0[VerticalAlignment.center] + 4 }
	            }
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
	    settingsCard(title: "Display", icon: .visible) {
	        SymairaFormRow(
	            "Show content preview thumbnails",
	            description: "When enabled, document and note cards show a text preview of the first content instead of a generic SF Symbol icon."
	        ) {
	            Toggle(isOn: $showThumbnails) { EmptyView() }
	                .toggleStyle(.symairaSwitch)
	                .accessibilityLabel("Show content preview thumbnails")
	                .alignmentGuide(.firstTextBaseline) { $0[VerticalAlignment.center] + 4 }
	        }
	    }
	}

    private var classificationRulesCard: some View {
        settingsCard(title: "Classification rules", icon: .filter) {
            if viewModel.isLoading && viewModel.rules.isEmpty {
                SymairaLoadingState("Loading rules…")
            } else if viewModel.rules.isEmpty {
                Text("No classification rules configured.")
                    .symairaText(.secondary)
            } else {
                VStack(spacing: 0) {
                    ForEach(viewModel.rules) { rule in
                        HStack(alignment: .top, spacing: 12) {
                            SymairaIconView(.filter, size: 16)
                                .foregroundStyle(SymairaTokens.ink2)
                            VStack(alignment: .leading, spacing: 4) {
                                Text(rule.pattern).symairaText(.subheading)
                                Text("\(rule.kind) → \(rule.value)")
                                    .symairaText(.secondary)
                            }
                            Spacer()
                            Button("Edit") { editingRule = rule; showingRuleEditor = true }
                                .symairaButtonStyle(.secondary)
                            Button { pendingDeleteRule = rule } label: { SymairaIconView(.delete, size: 16) }
                                .symairaButtonStyle(.secondary)
                                .accessibilityLabel("Delete Rule")
                                .help("Delete Rule")
                        }
                        .padding(.vertical, 10)
                        if rule.id != viewModel.rules.last?.id { SymairaFormDivider() }
                    }
                }
            }
        }
    }

    private var dryRunCard: some View {
        settingsCard(title: "Existing-document dry-run", icon: .documentSearch) {
            Text("Scan indexed Markdown notes without writing anything. Results include only safe metadata and matching rule IDs.")
                .symairaText(.secondary)
            HStack(spacing: SymairaSpacing.small) {
                TextField("Pattern", text: $viewModel.dryRunPattern)
                    .textFieldStyle(.symaira)
                TextField("Kind", text: $viewModel.dryRunKind)
                    .textFieldStyle(.symaira)
                    .frame(width: 150)
                TextField("Value", text: $viewModel.dryRunValue)
                    .textFieldStyle(.symaira)
            }
            Button {
                Task { _ = await viewModel.runDryRun() }
            } label: {
                Label("Run dry-run", symairaIcon: .play)
            }
            .symairaButtonStyle(.secondary)
            .disabled(viewModel.isPerformingOperation)

            if let result = viewModel.dryRunResult {
                Text("\(result.matchedDocuments) of \(result.totalDocuments) documents match")
                    .symairaText(.subheading)
                ForEach(result.matches) { match in
                Label("\(match.title) — \(match.notePath)", symairaIcon: .success)
                        .symairaText(.secondary)
                }
                ForEach(result.skipped) { skipped in
                Label("Skipped: \(skipped.notePath) — \(skipped.reason)", symairaIcon: .warning)
                        .symairaText(.callout, respectsForeground: false)
                        .foregroundStyle(SymairaTokens.warning)
                }
            }

            if let error = viewModel.lastError, viewModel.lastErrorKind == .validation {
                Label(error, symairaIcon: .error)
                    .symairaText(.callout, respectsForeground: false)
                    .foregroundStyle(SymairaTokens.critical)
            }
        }
    }

    private var mailRulesCard: some View {
        settingsCard(title: "Mail-ingest rules", icon: .mail) {
            if let error = viewModel.mailError {
                messageCard(title: "Mail configuration unavailable", message: error, icon: .pending) {
                    Button("Retry") { Task { await viewModel.loadMail() } }
                        .symairaButtonStyle(.secondary)
                }
            } else {
                HStack {
                    Text("Changes apply after restarting the mail watcher.")
                        .symairaText(.secondary)
                    Spacer()
                    Button("New Mail Account") { editingMail = nil; showingMailEditor = true }
                        .symairaButtonStyle(.secondary)
                }
                if viewModel.mailAccounts.isEmpty {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Mail import isn't set up yet.")
                            .symairaText(.bodyEmphasized)
                        Text("Click \"New Mail Account\" above to configure an IMAP account for automated email ingestion.")
                            .symairaText(.secondary)
                    }
                    .padding(.vertical, 4)
                } else {
                    ForEach(viewModel.mailAccounts, id: \.stableID) { account in
                        HStack(alignment: .top, spacing: 12) {
                            SymairaIconView(.mail, size: 16)
                                .foregroundStyle(SymairaTokens.ink2)
                            VStack(alignment: .leading, spacing: 4) {
                                Text(account.username + "@" + account.host).symairaText(.subheading)
                                Text("\(account.action) · \(account.folder) · password: \(account.passwordSecretKind ?? "unknown")")
                                    .symairaText(.secondary)
                                if !account.from.isEmpty || !account.subject.isEmpty {
                                    Text("Filters: \((account.from + account.subject).joined(separator: ", "))")
                                        .symairaText(.caption)
                                }
                            }
                            Spacer()
                            Button("Edit") { editingMail = account; showingMailEditor = true }
                                .symairaButtonStyle(.secondary)
                            Button { pendingDeleteMail = account } label: { SymairaIconView(.delete, size: 16) }
                                .symairaButtonStyle(.secondary)
                                .accessibilityLabel("Remove Mail Account")
                                .help("Remove Mail Account")
                        }
                        .padding(.vertical, 8)
                    }
                }
            }
        }
    }

    /// Shared settings card, built on the appkit form scaffold (issue #352):
    /// section label header + glass card body.
    private func settingsCard<Content: View>(title: String, icon: SymairaIcon, @ViewBuilder content: () -> Content) -> some View {
        SymairaFormSection(title) {
            content()
        }
    }

    /// Card offering the guided Paperless-ngx export import (issue #307).
    private var paperlessImportCard: some View {
        settingsCard(title: "Paperless-ngx import", icon: .download) {
            VStack(alignment: .leading, spacing: 10) {
                Text("Migrate documents from a Paperless-ngx export into this vault. Notes are created with contract frontmatter and the originals are archived under archive/paperless/.")
                    .symairaText(.secondary)
                Button {
                    showingPaperlessImport = true
                } label: {
                    Label("Import from Paperless…", symairaIcon: .download)
                }
                .symairaButtonStyle(.secondary)
            }
        }
    }

    /// Card that shows and controls the consume (watched inbox) folder.
    private var consumeFolderCard: some View {
        settingsCard(title: "Consume folder", icon: .inbox) {
            if let error = consumeFolderError {
                messageCard(title: "Could not load consume folder status", message: error, icon: .warning) {
                    Button("Retry") {
                        Task { await loadConsumeFolderStatus() }
                    }
                    .symairaButtonStyle(.secondary)
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
                                .symairaButtonStyle(.secondary)
                                Button("Cancel") {
                                    editingConsumeFolderPath = false
                                }
                                .symairaButtonStyle(.secondary)
                            }
                        }
                    } else {
                        HStack {
                            VStack(alignment: .leading, spacing: 4) {
                                Text(status.inboxPath)
                                    .symairaText(.subheading)
                                Text(status.configuredPath.isEmpty
                                     ? "Default path (not explicitly configured)"
                                     : "Configured in symdesk config")
                                    .symairaText(.caption)
                            }
                            Spacer()
                            Button("Change") {
                                editedConsumeFolderPath = status.inboxPath
                                editingConsumeFolderPath = true
                            }
                            .symairaButtonStyle(.secondary)
                        }
                    }

                    SymairaFormDivider()

                    // Watch status and toggle
                    HStack {
                        SymairaStatusLabel(watcher.isWatching ? "Running" : "Stopped", tone: watcher.isWatching ? .positive : .neutral)
                        Spacer()
                        if watcher.isWatching {
                            Button("Stop") {
                                watcher.stop()
                            }
                            .symairaButtonStyle(.secondary)
                        } else {
                            Button("Start") {
                                if let tool = core.tool {
                                    watcher.start(tool: tool, vaultPath: core.vaultPath)
                                }
                            }
                            .symairaButtonStyle(.secondary)
                            .disabled(core.tool == nil)
                        }
                    }

                    if !watcher.allEvents.isEmpty {
                        SymairaFormDivider()

                        // Recent activity
                        Text("Recent activity")
                            .symairaText(.subheading)

                        ForEach(watcher.recentActivity.prefix(5)) { event in
                            HStack(spacing: 8) {
                                SymairaIconView(iconForEvent(event.event), size: 14)
                                    .foregroundStyle(SymairaTokens.ink2)
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(event.event.replacingOccurrences(of: "_", with: " ").capitalized)
                                    .symairaText(.callout)
                                    Text(URL(fileURLWithPath: event.path).lastPathComponent)
                                        .symairaText(.caption)
                                        .lineLimit(1)
                                }
                                Spacer()
                                Text(formattedTimestamp(event.ts))
                                    .symairaText(.caption)
                            }
                            .padding(.vertical, 2)
                        }
                    }
                }
            } else {
                SymairaLoadingState("Loading consume folder status…")
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
        settingsCard(title: "AI", icon: .ai) {
            if let error = aiConfigError {
                messageCard(title: "Could not load AI configuration", message: error, icon: .warning) {
                    Button("Retry") {
                        Task { await loadAIConfig() }
                    }
                    .symairaButtonStyle(.secondary)
                }
            } else if aiConfig == nil {
                SymairaLoadingState("Loading AI configuration…")
                    .task { await loadAIConfig() }
            } else {
                VStack(alignment: .leading, spacing: 12) {
                    // AI off is a desk-level state, not a provider: the
                    // shared picker (appkit) manages only real providers,
                    // filtered to the ones the Go core implements.
                    SymairaFormRow("Enable AI") {
                        Toggle(isOn: Binding(
                            get: { aiProvider != "none" },
                            set: { setAIProvider($0 ? "ollama" : "none") }
                        )) { EmptyView() }
                        .toggleStyle(.symairaSwitch)
                        .accessibilityLabel("Enable AI")
                        .alignmentGuide(.firstTextBaseline) { $0[VerticalAlignment.center] + 4 }
                    }

                    if aiProvider != "none" {
                        if let catalog = aiProviderCatalog {
                            VStack(alignment: .leading, spacing: 6) {
                                Text("Provider")
                                    .symairaText(.secondary)
                                SymairaProviderPicker(selection: aiProviderBinding, catalog: catalog, title: "Provider")
                                    .pickerStyle(.segmented)
                                    .accessibilityLabel("AI provider")
                            }
                        } else {
                            Text("Provider catalog unavailable")
                                .symairaText(.secondary)
                        }
                    }

                    if aiProvider == "ollama" {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Ollama endpoint URL")
                                .symairaText(.secondary)
                            TextField("http://localhost:11434", text: $aiOllamaURL)
                                .textFieldStyle(.symaira)
                                .accessibilityLabel("Ollama endpoint URL")
                        }

                        VStack(alignment: .leading, spacing: 6) {
                            Text("Model")
                                .symairaText(.secondary)
                            if aiAvailableModels.isEmpty {
                                TextField("Enter an Ollama model", text: $aiModel)
                                    .textFieldStyle(.symaira)
                                    .accessibilityLabel("Ollama model")
                            } else {
                                Picker("", selection: $aiModel) {
                                    ForEach(aiAvailableModels, id: \.self) { name in
                                        Text(name).tag(name)
                                    }
                                }
                                .accessibilityLabel("Ollama model")
                            }
                        }
                    } else if aiProvider == "anthropic" {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("API key or symvault reference (op://…)")
                                .symairaText(.secondary)
                            SymairaProviderCredentialField(
                                providerID: aiProvider,
                                store: aiCredentialStore,
                                title: "Enter API key or symvault reference",
                                onCredentialChange: { aiTestResult = nil }
                            )
                            .id(aiProvider)
                            .accessibilityLabel("Anthropic API key or symvault reference")
                        }
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Model")
                                .symairaText(.secondary)
                            TextField("Enter an Anthropic model", text: $aiModel)
                                .textFieldStyle(.symaira)
                                .accessibilityLabel("Anthropic model")
                        }
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Max tokens")
                            .symairaText(.secondary)
                        TextField("Maximum response tokens", text: $aiMaxTokens)
                            .textFieldStyle(.symaira)
                            .frame(maxWidth: 160)
                            .accessibilityLabel("Maximum response tokens")
                    }

                    if let aiModelNotice {
                        SymairaNotice(message: aiModelNotice, tone: .informative)
                    }

                    HStack {
                        Button("Test connection") {
                            Task { await testAIConnection() }
                        }
                        .symairaButtonStyle(.secondary)
                        .disabled(aiIsTesting)

                        Button(aiIsSaving ? "Saving…" : "Save") {
                            Task { await saveAIConfig() }
                        }
                        .symairaButtonStyle(.secondary)
                        .disabled(aiIsSaving)

                        Spacer()
                    }

                    if aiIsTesting {
                        ProgressView("Testing connection…")
                    } else if let result = aiTestResult {
                        let providerName = aiProviderDisplayName(result.provider)
                        let modelName = aiModel.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false
                            ? aiModel
                            : "default model"
                        if result.ok {
                            VStack(alignment: .leading, spacing: SymairaSpacing.small) {
                                SymairaNotice(message: "Connected to \(providerName) using \(modelName).", tone: .positive)
                                if let models = result.models, !models.isEmpty {
                                    Text("Available models: \(models.joined(separator: ", "))")
                                        .symairaText(.secondary)
                                }
                            }
                        } else {
                            SymairaNotice(
                                message: "Connection failed for \(providerName) using \(modelName): \(result.error ?? "Connection failed."). Check the endpoint and credentials, then test again.",
                                tone: .critical
                            )
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

    private var aiProviderBinding: Binding<String> {
        Binding(
            get: { aiProvider },
            set: { setAIProvider($0) }
        )
    }

    private func aiProviderDisplayName(_ providerID: String) -> String {
        aiProviderCatalog?.provider(id: providerID)?.displayName ?? providerID.capitalized
    }

    private func setAIProvider(_ newProvider: String) {
        guard aiProvider != newProvider else { return }
        aiProvider = newProvider
        aiAvailableModels = []
        aiTestResult = nil

        let previousModel = aiModel.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !previousModel.isEmpty else {
            aiModelNotice = nil
            return
        }

        aiModel = ""
        aiModelNotice = "The previous model was cleared because the provider changed. Choose a model for \(aiProviderDisplayName(newProvider))."
    }

    private func compatibleAIModel(_ model: String, for provider: String) -> String {
        let trimmed = model.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return "" }

        // The Go defaults historically pair Ollama with an Anthropic model.
        // Never carry that mismatch into a new provider request.
        if provider == "ollama" && isAnthropicModel(trimmed) {
            return ""
        }
        if provider == "anthropic" && !isAnthropicModel(trimmed) {
            return ""
        }
        return trimmed
    }

    private func isAnthropicModel(_ model: String) -> Bool {
        let normalized = model.lowercased()
        return normalized.hasPrefix("claude") || normalized.contains("anthropic")
    }

    private func loadAIConfig() async {
        aiConfigError = nil
        do {
            let config = try await core.getAIConfig()
            aiConfig = config
            aiProvider = config.provider
            aiOllamaURL = config.ollamaURL
            let loadedModel = compatibleAIModel(config.model, for: config.provider)
            aiModel = loadedModel
            aiModelNotice = loadedModel.isEmpty && !config.model.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                ? "The saved model was cleared because it is not compatible with the \(aiProviderDisplayName(config.provider)) provider."
                : nil
            aiMaxTokens = String(config.maxTokens)
        } catch {
            aiConfigError = error.localizedDescription
        }
    }

    private func storedAICredential() -> String? {
        guard aiProvider == "anthropic" else { return nil }
        return try? aiCredentialStore.credential(for: aiProvider)
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
                apiKey: storedAICredential(),
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
                apiKey: storedAICredential(),
                maxTokens: Int(aiMaxTokens)
            )
            await loadAIConfig()
        } catch {
            aiConfigError = error.localizedDescription
        }
    }

    private func iconForEvent(_ event: String) -> SymairaIcon {
        switch event {
        case "file_added", "index_updated": return .documentAdd
        case "file_changed": return .edit
        case "file_removed": return .delete
        default: return .info
        }
    }

    private func formattedTimestamp(_ ts: Int64) -> String {
        let date = Date(timeIntervalSince1970: TimeInterval(ts))
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .abbreviated
        return formatter.localizedString(for: date, relativeTo: Date())
    }

    private func messageCard<Content: View>(title: String, message: String, icon: SymairaIcon, @ViewBuilder action: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: SymairaSpacing.small) {
            SymairaNotice(title: title, message: message, tone: icon == .server ? .informative : .warning)
            action()
        }
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

    private var isValid: Bool {
        !pattern.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !kind.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    var body: some View {
        SymairaSheetScaffold(
            title: existing == nil ? "New Classification Rule" : "Edit Classification Rule",
            dismiss: { dismiss() },
            primaryTitle: existing == nil ? "Add Rule" : "Save Rule",
            primaryDisabled: !isValid,
            primary: { onSave(pattern, kind, value) }
        ) {
            SymairaFormSection {
                SymairaFormRow("Pattern") {
                    TextField("Pattern", text: $pattern, prompt: Text("Text to match"))
                        .textFieldStyle(.symaira)
                        .frame(maxWidth: 300)
                }
                SymairaFormDivider()
                SymairaFormRow("Kind", description: "category, tag, correspondent or document_type") {
                    TextField("Kind", text: $kind, prompt: Text("category"))
                        .textFieldStyle(.symaira)
                        .frame(maxWidth: 300)
                }
                SymairaFormDivider()
                SymairaFormRow("Value") {
                    TextField("Value", text: $value, prompt: Text("Value to assign"))
                        .textFieldStyle(.symaira)
                        .frame(maxWidth: 300)
                }
            }
        }
        .frame(width: 520, height: 380)
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
        SymairaSheetScaffold(
            title: existing == nil ? "New Mail Account" : "Edit Mail Account",
            dismiss: { dismiss() },
            primaryTitle: existing == nil ? "Create Account" : "Save Account",
            primaryDisabled: host.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || username.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
            primary: { save() }
        ) {
                SymairaFormSection {
                    SymairaFormRow("IMAP Host") {
                        TextField("IMAP host", text: $host, prompt: Text("imap.example.com"))
                            .textFieldStyle(.symaira)
                            .frame(maxWidth: 300)
                    }
                    SymairaFormDivider()
                    SymairaFormRow("Port") {
                        TextField("Port", text: $port, prompt: Text("993"))
                            .textFieldStyle(.symaira)
                            .frame(maxWidth: 300)
                    }
                    SymairaFormDivider()
                    SymairaFormRow("Username") {
                        TextField("Username", text: $username)
                            .textFieldStyle(.symaira)
                            .frame(maxWidth: 300)
                    }
                    SymairaFormDivider()
                    SymairaFormRow(
                        "Password Secret Reference",
                        description: existing == nil ? nil : "Leave empty to preserve the stored reference."
                    ) {
                        SecureField(existing == nil ? "Password secret reference" : "New password secret reference", text: $passwordSecret)
                            .textFieldStyle(.symaira)
                            .frame(maxWidth: 300)
                    }
                    SymairaFormDivider()
                    SymairaFormRow("Folder") {
                        TextField("Folder", text: $folder, prompt: Text("INBOX"))
                            .textFieldStyle(.symaira)
                            .frame(maxWidth: 300)
                    }
                    SymairaFormDivider()
                    SymairaFormRow("From Filters", description: "Comma-separated") {
                        TextField("From filters", text: $from)
                            .textFieldStyle(.symaira)
                            .frame(maxWidth: 300)
                    }
                    SymairaFormDivider()
                    SymairaFormRow("Subject Filters", description: "Comma-separated") {
                        TextField("Subject filters", text: $subject)
                            .textFieldStyle(.symaira)
                            .frame(maxWidth: 300)
                    }
                    SymairaFormDivider()
                    SymairaFormRow("Action", description: "mark_seen or move") {
                        TextField("Action", text: $action, prompt: Text("mark_seen"))
                            .textFieldStyle(.symaira)
                            .frame(maxWidth: 300)
                    }
                    SymairaFormDivider()
                    SymairaFormRow("Move To") {
                        TextField("Move to", text: $moveTo)
                            .textFieldStyle(.symaira)
                            .frame(maxWidth: 300)
                    }
                    SymairaFormDivider()
                    SymairaFormRow("Require Attachment") {
                        Toggle(isOn: $hasAttachment) { EmptyView() }
                            .toggleStyle(.symairaSwitch)
                            .accessibilityLabel("Require attachment")
                            .alignmentGuide(.firstTextBaseline) { $0[VerticalAlignment.center] + 4 }
                    }
                    SymairaFormDivider()
                    SymairaFormRow("Archive .eml Message") {
                        Toggle(isOn: $archiveMail) { EmptyView() }
                            .toggleStyle(.symairaSwitch)
                            .accessibilityLabel("Archive .eml message")
                            .alignmentGuide(.firstTextBaseline) { $0[VerticalAlignment.center] + 4 }
                    }
                }
        }
        .frame(width: 600, height: 560)
    }

    private func save() {
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

    private func split(_ value: String) -> [String] {
        value.split(separator: ",").map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
    }
}
