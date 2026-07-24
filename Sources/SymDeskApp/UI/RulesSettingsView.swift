import SwiftUI
import SymairaTheme
import SymDeskCore
import SymairaIngestContract

struct RulesSettingsView: View {
	@EnvironmentObject private var core: DeskCore
    @StateObject private var viewModel: ClassificationRulesViewModel
    @State private var editingRule: ClassificationRule?
    @State private var editingMail: MailAccount?
    @State private var showingRuleEditor = false
    @State private var showingMailEditor = false
    @State private var pendingDeleteRule: ClassificationRule?
    @State private var pendingDeleteMail: MailAccount?
    @State private var showingChangeVaultConfirmation = false

    init(vaultPath: String? = nil) {
        _viewModel = StateObject(wrappedValue: ClassificationRulesViewModel(client: SymingestRulesClient(vaultPath: vaultPath)))
    }

    init(client: any ClassificationRulesClient) {
        _viewModel = StateObject(wrappedValue: ClassificationRulesViewModel(client: client))
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                header
				connectionCard
				if core.isRemote {
					messageCard(
						title: "Processing is managed by SymDesk Server",
						message: "Upload and queue status work from this Mac. Classification and mail-ingest settings remain server-side for now.",
						systemImage: "server.rack"
					) { EmptyView() }
				}
				if !core.isRemote {
                if let error = viewModel.lastError {
                    messageCard(title: "symingest unavailable or incompatible", message: error, systemImage: "exclamationmark.triangle") {
                        Button("Retry") { Task { await viewModel.load(); await viewModel.loadMail() } }
                            .buttonStyle(.bordered)
                            .controlSize(.small)
                    }
                }
                if let action = viewModel.lastActionMessage {
                    Label(action, systemImage: "checkmark.circle")
                        .foregroundStyle(SymairaTheme.goldPrimary)
                        .font(.callout)
                }
                classificationRulesCard
                dryRunCard
                mailRulesCard
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
                    .font(.largeTitle.bold())
                    .foregroundStyle(SymairaTheme.textPrimary)
                Text("Manage classification and mail-ingest behavior through symingest's versioned contract.")
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
						.font(.headline)
						.foregroundStyle(SymairaTheme.textPrimary)
					Text(core.isRemote ? "Vault, originals, index and OCR queue live on this server." : "The app and CLI read this vault directly on your Mac.")
						.font(.callout)
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
					Button("Change Vault…") {
						showingChangeVaultConfirmation = true
					}
					.buttonStyle(.bordered)
				}
			}
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
                                Text(rule.pattern).font(.headline)
                                Text("\(rule.kind) → \(rule.value)")
                                    .font(.callout)
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
                    .font(.headline)
                ForEach(result.matches) { match in
                    Label("\(match.title) — \(match.notePath)", systemImage: "checkmark.circle")
                        .font(.callout)
                        .foregroundStyle(SymairaTheme.textSecondary)
                }
                ForEach(result.skipped) { skipped in
                    Label("Skipped: \(skipped.notePath) — \(skipped.reason)", systemImage: "exclamationmark.triangle")
                        .font(.callout)
                        .foregroundStyle(SymairaTheme.goldSecondary)
                }
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
                    Text("Changes apply after restarting the symingest watcher.")
                        .font(.callout)
                        .foregroundStyle(SymairaTheme.textSecondary)
                    Spacer()
                    Button("New Mail Account") { editingMail = nil; showingMailEditor = true }
                        .buttonStyle(.borderedProminent)
                        .tint(SymairaTheme.goldPrimary)
                }
                if viewModel.mailAccounts.isEmpty {
                    Text("No IMAP accounts configured.")
                        .foregroundStyle(SymairaTheme.textSecondary)
                } else {
                    ForEach(viewModel.mailAccounts, id: \.stableID) { account in
                        HStack(alignment: .top, spacing: 12) {
                            Image(systemName: "envelope")
                                .foregroundStyle(SymairaTheme.goldPrimary)
                            VStack(alignment: .leading, spacing: 4) {
                                Text(account.username + "@" + account.host).font(.headline)
                                Text("\(account.action) · \(account.folder) · password: \(account.passwordSecretKind ?? "unknown")")
                                    .font(.callout)
                                    .foregroundStyle(SymairaTheme.textSecondary)
                                if !account.from.isEmpty || !account.subject.isEmpty {
                                    Text("Filters: \((account.from + account.subject).joined(separator: ", "))")
                                        .font(.caption)
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

    private func settingsCard<Content: View>(title: String, systemImage: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            Label(title, systemImage: systemImage)
                .font(.title3.bold())
                .foregroundStyle(SymairaTheme.textPrimary)
            content()
        }
        .padding(20)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.white.opacity(0.04))
        .clipShape(RoundedRectangle(cornerRadius: 12))
        .overlay { RoundedRectangle(cornerRadius: 12).stroke(SymairaTheme.borderGlass, lineWidth: 1) }
    }

    private func messageCard<Content: View>(title: String, message: String, systemImage: String, @ViewBuilder action: () -> Content) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: systemImage).foregroundStyle(SymairaTheme.goldSecondary)
            VStack(alignment: .leading, spacing: 6) {
                Text(title).font(.headline).foregroundStyle(SymairaTheme.textPrimary)
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
                .font(.title2.bold())
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
                .font(.title2.bold())
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
