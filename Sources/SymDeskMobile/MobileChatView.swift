import SwiftUI

/// Vault-grounded AI chat: ask a question, watch the answer stream in,
/// tap a citation to open the note it came from. Conversations persist
/// locally and are deletable. Provider/model configuration lives in the
/// in-app settings pane (never only an environment variable), and the
/// privacy implication — vault content leaves the device when a remote
/// provider answers — is stated before the first request.
struct MobileChatView: View {
    @EnvironmentObject private var vault: MobileVaultStore

    /// Optional context: the open note the question is about.
    var contextNote: MobileNote? = nil

    @State private var conversations: [MobileConversation] = []
    @State private var activeConversation: MobileConversation?
    @State private var input = ""
    @State private var isStreaming = false
    @State private var streamError: String?
    @State private var isSettingsPresented = false
    @State private var hasAcknowledgedPrivacy = false

    /// Active provider for the current answer — always visible in the UI.
    @State private var activeProviderName: String?
    @State private var onDeviceCapabilityNote: String?

    private let conversationStore = try? MobileConversationStore()

    var body: some View {
        NavigationStack {
            MobileBackdrop {
                VStack(spacing: 0) {
                    if !hasAcknowledgedPrivacy {
                        privacyNotice
                    }

                    if let conversation = activeConversation {
                        messageList(conversation)
                    } else {
                        emptyState
                    }

                    if let streamError {
                        errorBanner(streamError)
                    }

                    if let onDeviceCapabilityNote, !isStreaming {
                        Label(onDeviceCapabilityNote, systemImage: "cpu")
                            .font(.caption)
                            .foregroundStyle(MobileTheme.goldSoft)
                            .padding(.horizontal, 16)
                            .padding(.vertical, 6)
                    }

                    inputBar
                }
            }
            .navigationTitle("Ask your vault")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    if let activeProviderName {
                        Label(activeProviderName, systemImage: activeProviderName == "On-device" ? "cpu" : "server.rack")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(MobileTheme.textSecondary)
                            .padding(.horizontal, 10)
                            .padding(.vertical, 5)
                            .background(MobileTheme.card, in: Capsule())
                            .accessibilityLabel("AI provider: \(activeProviderName)")
                    }
                }
                ToolbarItem(placement: .topBarLeading) {
                    Menu {
                        ForEach(conversations) { conversation in
                            Button {
                                activeConversation = conversation
                                streamError = nil
                            } label: {
                                if conversation.id == activeConversation?.id {
                                    Label(conversation.title, systemImage: "checkmark")
                                } else {
                                    Text(conversation.title)
                                }
                            }
                        }
                        if !conversations.isEmpty {
                            Divider()
                            Button(role: .destructive) {
                                deleteActiveConversation()
                            } label: {
                                Label("Delete this conversation", systemImage: "trash")
                            }
                            Button(role: .destructive) {
                                deleteAllConversations()
                            } label: {
                                Label("Delete all conversations", systemImage: "trash.fill")
                            }
                        }
                    } label: {
                        Image(systemName: "ellipsis.circle")
                    }
                    .accessibilityLabel("Conversations")
                    .disabled(isStreaming)
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { isSettingsPresented = true } label: {
                        Image(systemName: "gearshape")
                    }
                    .accessibilityLabel("AI settings")
                    .disabled(isStreaming)
                }
            }
            .sheet(isPresented: $isSettingsPresented) {
                MobileAISettingsView()
                    .environmentObject(vault)
            }
            .task {
                conversations = await loadConversations()
                if let contextNote, let store = conversationStore {
                    // Opening a note in context starts a fresh conversation
                    // so "summarise this" applies to the open note.
                    let conversation = MobileConversation(
                        title: "About: \(contextNote.title)",
                        messages: [MobileChatMessage(
                            role: .assistant,
                            text: "I can answer questions about this note.\n\n_Tap a citation to open the original._"
                        )]
                    )
                    try? await store.save(conversation)
                    conversations.insert(conversation, at: 0)
                    activeConversation = conversation
                } else if conversations.isEmpty {
                    newConversation()
                } else {
                    activeConversation = conversations.first
                }
            }
        }
    }

    // MARK: - Privacy notice (before the first request)

    private var privacyNotice: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label("Your vault leaves this device", systemImage: "icloud.and.arrow.up")
                .font(.headline)
                .foregroundStyle(MobileTheme.textPrimary)
            Text("Questions are answered by the AI provider configured in Settings — the relevant vault excerpts are sent to that provider (or to your self-hosted server, which forwards them). Only ask questions you are comfortable sharing.")
                .font(.caption)
                .foregroundStyle(MobileTheme.textSecondary)
            Button("I understand") {
                hasAcknowledgedPrivacy = true
            }
            .font(.caption.weight(.semibold))
            .buttonStyle(.bordered)
            .controlSize(.small)
            .tint(MobileTheme.gold)
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .mobileLiquidGlass(cornerRadius: 16)
        .padding(.horizontal, 16)
        .padding(.top, 10)
    }

    // MARK: - States

    private var emptyState: some View {
        VStack(spacing: 14) {
            Image(systemName: "bubble.left.and.bubble.right")
                .font(.system(size: 40))
                .foregroundStyle(MobileTheme.gold)
            Text("Ask your vault")
                .font(.title3.bold())
                .foregroundStyle(MobileTheme.textPrimary)
            Text("“What is due this month?” or “Summarise the open invoice.” Answers stream in with citations you can tap to open the source.")
                .font(.subheadline)
                .foregroundStyle(MobileTheme.textSecondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 32)
            if !vault.isRemote {
                Text("Tip: this mode uses the Files/iCloud vault — connect a server in Settings to unlock grounded AI answers.")
                    .font(.caption)
                    .foregroundStyle(MobileTheme.goldSoft)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 32)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func messageList(_ conversation: MobileConversation) -> some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 12) {
                    ForEach(conversation.messages) { message in
                        MessageBubble(message: message) { path in
                            vault.pendingOpenPath = path
                        }
                    }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 14)
                .frame(maxWidth: 720)
                .frame(maxWidth: .infinity)
            }
            .onChange(of: conversation.messages.count) { _, _ in
                if let last = conversation.messages.last {
                    proxy.scrollTo(last.id, anchor: .bottom)
                }
            }
        }
    }

    private func errorBanner(_ message: String) -> some View {
        Label(message, systemImage: "exclamationmark.triangle.fill")
            .font(.caption)
            .foregroundStyle(.red)
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
    }

    // MARK: - Input

    private var inputBar: some View {
        HStack(spacing: 10) {
            TextField(contextNote == nil ? "Ask your vault…" : "Ask about “\(contextNote!.title)”…", text: $input, axis: .vertical)
                .textFieldStyle(.plain)
                .lineLimit(1...4)
                .padding(.horizontal, 12)
                .padding(.vertical, 9)
                .background(MobileTheme.card, in: RoundedRectangle(cornerRadius: 14))
                .disabled(isStreaming)

            Button {
                Task { await send() }
            } label: {
                if isStreaming {
                    ProgressView()
                        .frame(width: 34, height: 34)
                } else {
                    Image(systemName: "arrow.up.circle.fill")
                        .font(.system(size: 30))
                }
            }
            .buttonStyle(.plain)
            .foregroundStyle(input.trimmingCharacters(in: .whitespaces).isEmpty || isStreaming ? MobileTheme.textMuted : MobileTheme.gold)
            .disabled(input.trimmingCharacters(in: .whitespaces).isEmpty || isStreaming)
            .accessibilityLabel("Send")
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .background(.ultraThinMaterial)
    }

    private func send() async {
        let question = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !question.isEmpty, !isStreaming else { return }

        // Automatic provider selection: server when reachable, on-device
        // otherwise (fallback also covers Files/iCloud mode). The active
        // provider is always visible in the UI.
        let selection = MobileAIProviderFactory.select(
            connection: MobileServerConfig.connection(),
            vaultNotes: vault.notes
        )
        guard let provider = selection.provider else {
            streamError = selection.unavailableReason ?? "No AI provider is available."
            return
        }
        activeProviderName = provider.displayName
        if provider.isOnDevice {
            streamError = nil
            onDeviceCapabilityNote = (provider as? MobileOnDeviceAIProvider)?.capabilityNote ?? "On-device model — answers are shorter and weaker than the server model."
        } else {
            onDeviceCapabilityNote = nil
        }

        if activeConversation == nil { newConversation() }
        guard var conversation = activeConversation else { return }

        let contextPath = contextNote?.path
        let userMessage = MobileChatMessage(
            role: .user,
            text: question,
            createdAt: Date(),
            contextPath: contextPath
        )
        conversation.messages.append(userMessage)
        conversation.updatedAt = Date()
        activeConversation = conversation
        persist(conversation)
        input = ""
        streamError = nil
        isStreaming = true

        // Streaming placeholder: tokens accumulate into this message.
        let placeholder = MobileChatMessage(role: .assistant, text: "")
        conversation.messages.append(placeholder)
        activeConversation = conversation

        let questionForServer = (contextPath.map { "Context note: \($0)\n\n" } ?? "") + question

        do {
            // Losing connectivity mid-session falls back to the on-device
            // provider automatically — no manual switch. The server is
            // tried first whenever a connection is configured.
            try await answer(with: provider, query: questionForServer, placeholderID: placeholder.id)
        } catch {
            if !provider.isOnDevice, let onDevice = fallbackProvider() {
                activeProviderName = onDevice.displayName
                onDeviceCapabilityNote = onDevice.capabilityNote
                streamError = nil
                do {
                    try await answer(with: onDevice, query: questionForServer, placeholderID: placeholder.id)
                } catch {
                    markFailed(placeholderID: placeholder.id, error: error)
                }
            } else {
                markFailed(placeholderID: placeholder.id, error: error)
            }
        }
        isStreaming = false
    }

    /// Runs one provider pass over the streaming placeholder.
    private func answer(with provider: MobileAIProvider, query: String, placeholderID: UUID) async throws {
        try await provider.ask(query: query) { event in
            Task { @MainActor in
                guard var current = activeConversation,
                      let index = current.messages.firstIndex(where: { $0.id == placeholderID }) else { return }
                switch event.type {
                case .answer:
                    current.messages[index].text += event.text ?? ""
                case .citation:
                    let citation = MobileChatCitation(
                        path: event.path ?? "",
                        title: event.title ?? "",
                        snippet: event.snippet ?? "",
                        score: event.score ?? 0
                    )
                    if !citation.path.isEmpty, !current.messages[index].citations.contains(citation) {
                        current.messages[index].citations.append(citation)
                    }
                case .tool, .done:
                    break
                }
                current.updatedAt = Date()
                activeConversation = current
            }
        }
        // Stream ended normally.
        if var current = activeConversation,
           let index = current.messages.firstIndex(where: { $0.id == placeholderID }) {
            if current.messages[index].text.isEmpty {
                current.messages[index].text = "_No answer received._"
            }
            current.updatedAt = Date()
            activeConversation = current
            persist(current)
        }
    }

    /// The on-device provider to fall back to, when the device supports it.
    private func fallbackProvider() -> MobileOnDeviceAIProvider? {
        let onDevice = MobileOnDeviceAIProvider(vaultNotes: vault.notes)
        guard onDevice.isAvailable else { return nil }
        return onDevice
    }

    private func markFailed(placeholderID: UUID, error: Error) {
        if var current = activeConversation,
           let index = current.messages.firstIndex(where: { $0.id == placeholderID }) {
            current.messages[index].text = "_Request failed._"
            current.updatedAt = Date()
            activeConversation = current
            persist(current)
        }
        streamError = error.localizedDescription
    }

    // MARK: - Conversation management

    private func newConversation() {
        let conversation = MobileConversation(title: "New conversation")
        activeConversation = conversation
        persist(conversation)
        conversations.insert(conversation, at: 0)
    }

    private func deleteActiveConversation() {
        guard let active = activeConversation else { return }
        Task {
            try? await conversationStore?.delete(id: active.id)
            conversations = await loadConversations()
            activeConversation = conversations.first
        }
    }

    private func deleteAllConversations() {
        Task {
            try? await conversationStore?.deleteAll()
            conversations = []
            activeConversation = nil
            newConversation()
        }
    }

    private func persist(_ conversation: MobileConversation) {
        Task {
            try? await conversationStore?.save(conversation)
            conversations = await loadConversations()
        }
    }

    private func loadConversations() async -> [MobileConversation] {
        guard let store = conversationStore else { return [] }
        return (try? await store.all()) ?? []
    }
}

// MARK: - Message bubble

private struct MessageBubble: View {
    let message: MobileChatMessage
    let onCitationTap: (String) -> Void

    var body: some View {
        HStack {
            if message.role == .user { Spacer(minLength: 48) }
            VStack(alignment: .leading, spacing: 8) {
                if message.role == .user {
                    Text(message.text)
                        .font(.body)
                        .foregroundStyle(.black)
                        .padding(12)
                        .background(MobileTheme.gold, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                } else {
                    if let contextPath = message.contextPath {
                        Label("Context: \(contextPath)", systemImage: "doc.text")
                            .font(.caption2)
                            .foregroundStyle(MobileTheme.textMuted)
                    }
                    if message.text.isEmpty {
                        ProgressView()
                            .controlSize(.small)
                    } else {
                        Text(renderedMarkdown(message.text))
                            .font(.body)
                            .foregroundStyle(MobileTheme.textPrimary)
                            .textSelection(.enabled)
                    }
                    if !message.citations.isEmpty {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Sources")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(MobileTheme.textMuted)
                            ForEach(message.citations) { citation in
                                Button {
                                    onCitationTap(citation.path)
                                } label: {
                                    HStack(spacing: 8) {
                                        Image(systemName: "doc.text.magnifyingglass")
                                            .foregroundStyle(MobileTheme.gold)
                                        Text(citation.title.isEmpty ? citation.path : citation.title)
                                            .font(.caption.weight(.medium))
                                            .lineLimit(1)
                                        Spacer()
                                        Image(systemName: "chevron.right")
                                            .font(.caption2)
                                    }
                                    .foregroundStyle(MobileTheme.textPrimary)
                                    .padding(10)
                                    .background(MobileTheme.card, in: RoundedRectangle(cornerRadius: 12))
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                }
            }
            if message.role == .assistant { Spacer(minLength: 48) }
        }
    }

    private func renderedMarkdown(_ source: String) -> AttributedString {
        (try? AttributedString(
            markdown: source,
            options: AttributedString.MarkdownParsingOptions(interpretedSyntax: .full)
        )) ?? AttributedString(source)
    }
}
