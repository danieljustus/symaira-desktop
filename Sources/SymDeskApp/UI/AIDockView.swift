import SwiftUI
import SymairaTheme
import SymDeskCore

enum ChatEntry: Identifiable {
    case user(id: UUID, text: String)
    case answer(id: UUID, text: String)
    case citation(id: UUID, path: String, title: String, snippet: String, score: Double?)
    // status: legacy MCP status event ("running"/"done"/"error"); otherwise
    // "call" (tool invoked, inputs present) or "result" (tool output).
    case tool(id: UUID, toolName: String, status: String, iteration: Int?, inputs: String?, output: String?, truncated: Bool?)

    var id: UUID {
        switch self {
        case .user(let id, _), .answer(let id, _), .citation(let id, _, _, _, _), .tool(let id, _, _, _, _, _, _):
            return id
        }
    }
}

struct AIDockView: View {
    @EnvironmentObject var core: DeskCore

    @State private var query: String = ""
    @State private var chatHistory: [ChatEntry] = []
    @State private var isThinking = false
    @State private var agentMode = true

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "sparkles")
                    .foregroundStyle(SymairaTheme.goldPrimary)
                Text("AI Dock")
                    .font(.headline)
                    .foregroundColor(SymairaTheme.textPrimary)
                Spacer()
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)

            Divider()

            ScrollViewReader { proxy in
                ScrollView {
                    VStack(alignment: .leading, spacing: 12) {
                        ForEach(chatHistory) { entry in
                            chatRow(entry)
                        }

                        if isThinking {
                            HStack {
                                ProgressView()
                                    .tint(SymairaTheme.goldPrimary)
                                    .padding()
                                Spacer()
                            }
                        }
                    }
                    .padding()
                }
                .onChange(of: chatHistory.count) {
                    scrollToBottom(proxy: proxy)
                }
            }

            Divider()

            HStack(spacing: 8) {
                Toggle(isOn: $agentMode) {
                    Text("Agent")
                        .font(.caption)
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
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
            .padding(12)
        }
        .frame(minWidth: 280, idealWidth: 300, maxWidth: 420)
        .background(.clear)
    }

    @ViewBuilder
    private func chatRow(_ entry: ChatEntry) -> some View {
        switch entry {
        case .user(_, let text):
            HStack {
                Spacer()
                Text(text)
                    .foregroundColor(SymairaTheme.textPrimary)
                    .padding()
                    .background(SymairaTheme.goldPrimary.opacity(0.12), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    .overlay {
                        RoundedRectangle(cornerRadius: 14, style: .continuous)
                            .stroke(SymairaTheme.goldPrimary.opacity(0.3), lineWidth: 1)
                    }
                    .frame(maxWidth: 250, alignment: .trailing)
            }

        case .answer(let id, let text):
            VStack(alignment: .leading, spacing: 6) {
                HStack(alignment: .top) {
                    Text(LocalizedStringKey(text))
                        .foregroundColor(SymairaTheme.textPrimary)
                        .padding()
                        .symDeskLiquidGlass(cornerRadius: 14)
                        .frame(maxWidth: 250, alignment: .leading)
                    Spacer()
                    Button {
                        copyToClipboard(text)
                    } label: {
                        Image(systemName: "doc.on.doc")
                            .font(.caption)
                            .foregroundColor(SymairaTheme.textMuted)
                    }
                    .buttonStyle(.borderless)
                    .help("Copy answer")
                }
                if text.contains("AI feature not configured") {
                    Button {
                        NotificationCenter.default.post(name: .openRulesSettings, object: "ai")
                    } label: {
                        Label("Open AI Settings", systemImage: "gearshape")
                            .font(.caption)
                    }
                    .buttonStyle(.borderless)
                    .foregroundColor(SymairaTheme.goldSecondary)
                }
            }
            .id(id)

        case .citation(_, let path, let title, let snippet, let score):
            VStack(alignment: .leading, spacing: 4) {
                HStack {
                    Image(systemName: "doc.text")
                        .font(.caption)
                        .foregroundColor(SymairaTheme.goldPrimary)
                    Text(title.isEmpty ? path : title)
                        .font(.caption.bold())
                        .foregroundColor(SymairaTheme.textPrimary)
                    Spacer()
                    Button {
                        openFile(path)
                    } label: {
                        Image(systemName: "arrow.up.right.square")
                            .font(.caption)
                            .foregroundColor(SymairaTheme.goldSecondary)
                    }
                    .buttonStyle(.borderless)
                    .help("Open note")
                }
                Text(snippet)
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textSecondary)
                    .lineLimit(3)
                if let score {
                    Text(String(format: "Score: %.2f", score))
                        .font(.caption2)
                        .foregroundColor(SymairaTheme.textMuted)
                }
            }
            .padding(8)
            .background(SymairaTheme.goldPrimary.opacity(0.06))
            .cornerRadius(8)
            .overlay(
                RoundedRectangle(cornerRadius: 8)
                    .stroke(SymairaTheme.borderGlassHover, lineWidth: 1)
            )
            .frame(maxWidth: 280, alignment: .leading)

        case .tool(_, let toolName, let status, let iteration, let inputs, let output, let truncated):
            if status == "call" {
                // Agentic loop (issue #317): the model invoked a read-only
                // tool — show name + inputs instead of a bare spinner.
                HStack(spacing: 6) {
                    Image(systemName: "wand.and.stars")
                        .font(.caption)
                        .foregroundColor(SymairaTheme.goldPrimary)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(iterationLabel(iteration, toolName: toolName))
                            .font(.caption)
                            .foregroundColor(SymairaTheme.textSecondary)
                        if let inputs, !inputs.isEmpty, inputs != "{}" {
                            Text(inputs)
                                .font(.system(size: 10, design: .monospaced))
                                .foregroundColor(SymairaTheme.textMuted)
                                .lineLimit(2)
                                .truncationMode(.middle)
                        }
                    }
                }
                .padding(6)
                .background(SymairaTheme.bgCard)
                .cornerRadius(6)
            } else if status == "result" {
                HStack(spacing: 6) {
                    Image(systemName: "checkmark.circle.fill")
                        .font(.caption)
                        .foregroundColor(SymairaTheme.goldPrimary)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(iterationLabel(iteration, toolName: toolName))
                            .font(.caption)
                            .foregroundColor(SymairaTheme.textSecondary)
                        if let output {
                            Text(output)
                                .font(.system(size: 10, design: .monospaced))
                                .foregroundColor(SymairaTheme.textMuted)
                                .lineLimit(3)
                                .truncationMode(.tail)
                            if truncated == true {
                                Text("(output truncated for the model)")
                                    .font(.caption2)
                                    .foregroundColor(.orange)
                            }
                        }
                    }
                }
                .padding(6)
                .background(SymairaTheme.bgCard)
                .cornerRadius(6)
            } else if status == "usage" {
                // Terminal event footer: token usage / context window.
                HStack(spacing: 6) {
                    Image(systemName: "bolt.fill")
                        .font(.caption2)
                        .foregroundColor(SymairaTheme.textMuted)
                    Text(toolName)
                        .font(.caption2)
                        .foregroundColor(SymairaTheme.textMuted)
                }
                .padding(4)
            } else {
                HStack(spacing: 6) {
                    if status == "running" {
                        ProgressView()
                            .controlSize(.small)
                            .tint(SymairaTheme.goldPrimary)
                    } else if status == "done" {
                        Image(systemName: "checkmark.circle.fill")
                            .font(.caption)
                            .foregroundColor(SymairaTheme.goldPrimary)
                    } else if status == "error" {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .font(.caption)
                            .foregroundColor(.orange)
                    }
                    Text("\(toolName): \(status)")
                        .font(.caption)
                        .foregroundColor(SymairaTheme.textSecondary)
                }
                .padding(6)
                .background(SymairaTheme.bgCard)
                .cornerRadius(6)
            }
        }
    }

    private func iterationLabel(_ iteration: Int?, toolName: String) -> String {
        if let iteration {
            return "Iteration \(iteration) · \(toolName)"
        }
        return toolName
    }

    private func scrollToBottom(proxy: ScrollViewProxy) {
        guard let last = chatHistory.last else { return }
        withAnimation {
            proxy.scrollTo(last.id, anchor: .bottom)
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
                let stream = core.ask(query: q, agent: agentMode)
                isThinking = false

                for try await event in stream {
                    switch event.type {
                    case .answer:
                        let text = event.text ?? ""
                        if let idx = chatHistory.lastIndex(where: {
                            if case .answer = $0 { return true }
                            return false
                        }),
                           case .answer(let existingId, var existingText) = chatHistory[idx] {
                            existingText += text
                            chatHistory[idx] = .answer(id: existingId, text: existingText)
                        } else {
                            chatHistory.append(.answer(id: UUID(), text: text))
                        }

                    case .citation:
                        chatHistory.append(.citation(
                            id: UUID(),
                            path: event.path ?? "",
                            title: event.title ?? "",
                            snippet: event.snippet ?? "",
                            score: event.score
                        ))

                    case .tool:
                        if let inputs = event.toolInputs {
                            // Agentic loop: the model invoked a tool.
                            chatHistory.append(.tool(
                                id: UUID(),
                                toolName: event.toolName ?? "unknown",
                                status: "call",
                                iteration: event.iteration,
                                inputs: inputs,
                                output: nil,
                                truncated: nil
                            ))
                        } else if event.toolOutput != nil {
                            // Agentic loop: tool finished, output reported.
                            chatHistory.append(.tool(
                                id: UUID(),
                                toolName: event.toolName ?? "unknown",
                                status: "result",
                                iteration: event.iteration,
                                inputs: nil,
                                output: event.toolOutput,
                                truncated: event.toolOutputTruncated
                            ))
                        } else {
                            chatHistory.append(.tool(
                                id: UUID(),
                                toolName: event.toolName ?? "unknown",
                                status: event.status ?? "unknown",
                                iteration: nil,
                                inputs: nil,
                                output: nil,
                                truncated: nil
                            ))
                        }

                    case .done:
                        if let usage = event.tokenUsage, usage > 0 {
                            let window = event.contextWindow ?? 0
                            let label = window > 0 ? "usage: \(usage) tokens · context \(window)" : "usage: \(usage) tokens"
                            chatHistory.append(.tool(
                                id: UUID(),
                                toolName: label,
                                status: "usage",
                                iteration: nil,
                                inputs: nil,
                                output: nil,
                                truncated: nil
                            ))
                        }
                    }
                }
            } catch {
                isThinking = false
                chatHistory.append(.answer(id: UUID(), text: "\n\n**Error:** \(error.localizedDescription)"))
            }
        }
    }

    private func copyToClipboard(_ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }

    private func openFile(_ path: String) {
        let vaultRoot = core.vaultPath ?? ""
        let absPath = (vaultRoot as NSString).appendingPathComponent(path)
        NSWorkspace.shared.open(URL(fileURLWithPath: absPath))
    }
}
