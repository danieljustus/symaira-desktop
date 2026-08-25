import SwiftUI
import SymairaTheme
import SymDeskCore

struct AIDockView: View {
    @EnvironmentObject var core: DeskCore

    let context: DeskChatContext?

    @State private var query: String = ""
    @State private var chatHistory: [ChatEntry] = []
    @State private var isThinking = false
    @State private var agentMode = true
    @State private var includeContext = false

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "sparkles")
                    .foregroundStyle(SymairaTheme.goldPrimary)
                Text("AI Dock")
                    .symairaText(.subheading)
                    .foregroundColor(SymairaTheme.textPrimary)
                Spacer()
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)

            Divider()

            ChatTranscriptView(chatHistory: chatHistory, isThinking: isThinking, onOpenFile: openFile)

            Divider()

            VStack(spacing: 8) {
                if let context, !context.isEmpty {
                    HStack(spacing: 8) {
                        Image(systemName: "doc.text.magnifyingglass")
                            .foregroundStyle(SymairaTheme.goldPrimary)
                        Text("Context: \(context.summary)")
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textSecondary)
                            .lineLimit(1)
                        if context.selectionText != nil {
                            Text("Selection")
                                .symairaText(.caption)
                                .foregroundColor(SymairaTheme.goldSecondary)
                        }
                        Spacer(minLength: 4)
                        Toggle("Send", isOn: $includeContext)
                            .toggleStyle(.switch)
                            .controlSize(.small)
                            .help("Include the active document context in the next AI request")
                    }
                    .padding(.horizontal, 10)
                    .padding(.vertical, 7)
                    .background(SymairaTheme.bgCardHover.opacity(0.72))
                    .overlay(
                        RoundedRectangle(cornerRadius: 8)
                            .stroke(SymairaTheme.borderGlass, lineWidth: 1)
                    )
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel("Context \(context.summary)")
                    .accessibilityValue(includeContext ? "On" : "Off")
                }

                HStack(spacing: 8) {
                    Toggle(isOn: $agentMode) {
                        Text("Agent")
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textSecondary)
                    }
                    .toggleStyle(.switch)
                    .controlSize(.small)
                    .help("Run the bounded agentic tool loop (read-only tools) instead of a one-shot answer")

                    TextField("Ask about your vault…", text: $query)
                        .textFieldStyle(.plain)
                        .onSubmit {
                            submitQuery()
                        }

                    Button(action: submitQuery) {
                        Image(systemName: "paperplane.fill")
                            .foregroundColor(SymairaTheme.goldPrimary)
                    }
                    .buttonStyle(.plain)
                    .disabled(query.trimmingCharacters(in: .whitespaces).isEmpty || isThinking)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
            .padding(12)
        }
        .frame(minWidth: 280, idealWidth: 300, maxWidth: 420)
        .background(.clear)
    }

    private func submitQuery() {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return }

        let requestContext = includeContext ? context : nil
        let requestedAgentMode = agentMode
        query = ""
        chatHistory.append(.user(id: UUID(), text: q))
        isThinking = true

        Task {
            do {
                let stream = core.ask(query: q, context: requestContext, agent: requestedAgentMode)
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

    private func openFile(_ path: String) {
        let vaultRoot = core.vaultPath ?? ""
        let absPath = (vaultRoot as NSString).appendingPathComponent(path)
        NSWorkspace.shared.open(URL(fileURLWithPath: absPath))
    }
}
