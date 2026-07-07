import SwiftUI
import SymDeskCore

struct AIDockView: View {
    @EnvironmentObject var core: DeskCore

    @State private var query: String = ""
    @State private var chatHistory: [(id: UUID, role: String, text: String)] = []
    @State private var isThinking = false

    var body: some View {
        VStack {
            Text("AI Dock")
                .font(.headline)
                .padding(.top)

            Divider()

            ScrollViewReader { proxy in
                ScrollView {
                    VStack(alignment: .leading, spacing: 12) {
                        ForEach(chatHistory, id: \.id) { msg in
                            HStack {
                                if msg.role == "user" {
                                    Spacer()
                                    Text(msg.text)
                                        .padding()
                                        .background(Color.accentColor.opacity(0.2))
                                        .cornerRadius(12)
                                        .frame(maxWidth: 250, alignment: .trailing)
                                } else {
                                    Text(LocalizedStringKey(msg.text))
                                        .padding()
                                        .background(Color.gray.opacity(0.1))
                                        .cornerRadius(12)
                                        .frame(maxWidth: 250, alignment: .leading)
                                    Spacer()
                                }
                            }
                        }

                        if isThinking {
                            HStack {
                                ProgressView()
                                    .padding()
                                Spacer()
                            }
                        }
                    }
                    .padding()
                }
                .onChange(of: chatHistory.count) { _ in
                    if let last = chatHistory.last {
                        withAnimation {
                            proxy.scrollTo(last.id, anchor: .bottom)
                        }
                    }
                }
                .onChange(of: chatHistory.last?.text) { _ in
                    if let last = chatHistory.last {
                        withAnimation {
                            proxy.scrollTo(last.id, anchor: .bottom)
                        }
                    }
                }
            }

            Divider()

            HStack {
                TextField("Ask about your vault...", text: $query)
                    .textFieldStyle(RoundedBorderTextFieldStyle())
                    .onSubmit {
                        submitQuery()
                    }

                Button(action: submitQuery) {
                    Image(systemName: "paperplane.fill")
                }
                .disabled(query.trimmingCharacters(in: .whitespaces).isEmpty || isThinking)
            }
            .padding()
        }
        .frame(minWidth: 300, idealWidth: 300, maxWidth: .infinity)
    }

    private func submitQuery() {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return }

        query = ""
        let userMsgID = UUID()
        chatHistory.append((id: userMsgID, role: "user", text: q))

        let aiMsgID = UUID()
        chatHistory.append((id: aiMsgID, role: "ai", text: ""))
        isThinking = true

        Task {
            do {
                let stream = core.ask(query: q)
                isThinking = false

                for try await chunk in stream {
                    if let idx = chatHistory.firstIndex(where: { $0.id == aiMsgID }) {
                        chatHistory[idx].text += chunk
                    }
                }
            } catch {
                isThinking = false
                if let idx = chatHistory.firstIndex(where: { $0.id == aiMsgID }) {
                    chatHistory[idx].text += "\n\n**Error:** \(error.localizedDescription)"
                }
            }
        }
    }
}
