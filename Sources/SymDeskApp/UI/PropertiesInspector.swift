import SwiftUI
import SymairaTheme
import SymDeskCore

/// Typed frontmatter properties inspector for a single note. Infers a control
/// per property (text, number, date, status, tags, relation) and routes every
/// edit through the shared `props edit` mutation path in the core.
struct PropertiesInspector: View {
    let notePath: String
    var onChanged: (() -> Void)? = nil
    var onTagClick: ((String) -> Void)? = nil
    /// Existing tags in the vault, used for autocomplete when editing the
    /// tags property (issue #306). Empty disables suggestions.
    var allTags: [String] = []

    @EnvironmentObject var core: DeskCore

    @State private var properties: [String: String] = [:]
    @State private var inverseRelations: [InverseRelation] = []
    @State private var newKey = ""
    @State private var newValue = ""
    @State private var isLoading = false
    @State private var errorMessage: String?

    enum PropertyKind {
        case text
        case number
        case date
        case status
        case tags
        case relation
    }

    static let statusValues = ["open", "paid", "submitted", "done", "needs_review", "waiting_for_reply"]

    private var sortedKeys: [String] {
        properties.keys.sorted()
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Text("Properties")
                    .font(.headline)
                    .foregroundColor(SymairaTheme.goldPrimary)

                if isLoading && properties.isEmpty {
                    ProgressView().tint(SymairaTheme.goldPrimary)
                } else if properties.isEmpty {
                    Text("No properties yet.")
                        .font(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                }

                ForEach(sortedKeys, id: \.self) { key in
                    propertyRow(key: key, value: properties[key] ?? "")
                }

                HStack {
                    TextField("Property", text: $newKey)
                        .textFieldStyle(RoundedBorderTextFieldStyle())
                    TextField("Value", text: $newValue)
                        .textFieldStyle(RoundedBorderTextFieldStyle())
                    Button {
                        let key = newKey.trimmingCharacters(in: .whitespaces)
                        guard !key.isEmpty else { return }
                        Task {
                            await saveProperty(key: key, value: newValue)
                            newKey = ""
                            newValue = ""
                        }
                    } label: {
                        Image(systemName: "plus.circle.fill")
                    }
                    .buttonStyle(.plain)
                    .disabled(newKey.trimmingCharacters(in: .whitespaces).isEmpty)
                }

                if !inverseRelations.isEmpty {
                    Divider().overlay(SymairaTheme.borderGlass)
                    Text("Linked From")
                        .font(.headline)
                        .foregroundColor(SymairaTheme.goldPrimary)
                    ForEach(inverseRelations) { relation in
                        HStack {
                            Image(systemName: relation.property == "_link" ? "link" : "tag")
                                .foregroundColor(SymairaTheme.textMuted)
                            VStack(alignment: .leading) {
                                Text(relation.title.isEmpty ? relation.source : relation.title)
                                    .foregroundColor(SymairaTheme.textPrimary)
                                Text(relation.property == "_link" ? "wikilink" : "via \(relation.property)")
                                    .font(.caption)
                                    .foregroundColor(SymairaTheme.textMuted)
                            }
                        }
                    }
                }

                if let errorMessage {
                    Text(errorMessage)
                        .font(.caption)
                        .foregroundColor(.red)
                }
            }
            .padding()
        }
        .task(id: notePath) { await load() }
    }

    static func kind(forKey key: String, value: String) -> PropertyKind {
        if key == "status" { return .status }
        if key == "tags" { return .tags }
        if value.contains("[[") { return .relation }
        if parseISODate(value) != nil { return .date }
        if !value.isEmpty, Double(value) != nil { return .number }
        return .text
    }

    static func parseISODate(_ value: String) -> Date? {
        guard value.count == 10 else { return nil }
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyy-MM-dd"
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = TimeZone(identifier: "UTC")
        return formatter.date(from: value)
    }

    static func formatISODate(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyy-MM-dd"
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = TimeZone(identifier: "UTC")
        return formatter.string(from: date)
    }

    @ViewBuilder
    private func propertyRow(key: String, value: String) -> some View {
        let kind = Self.kind(forKey: key, value: value)
        VStack(alignment: .leading, spacing: 2) {
            Text(key)
                .font(.caption)
                .foregroundColor(SymairaTheme.textMuted)
            switch kind {
            case .status:
                Picker("", selection: Binding(
                    get: { value },
                    set: { newValue in Task { await saveProperty(key: key, value: newValue) } }
                )) {
                    if !Self.statusValues.contains(value) {
                        Text(value).tag(value)
                    }
                    ForEach(Self.statusValues, id: \.self) { Text($0).tag($0) }
                }
                .labelsHidden()
            case .date:
                DatePicker("", selection: Binding(
                    get: { Self.parseISODate(value) ?? Date() },
                    set: { newDate in Task { await saveProperty(key: key, value: Self.formatISODate(newDate)) } }
                ), displayedComponents: .date)
                .labelsHidden()
            case .number:
                CommitTextField(value: value, monospaced: true) { text in
                    guard Double(text) != nil || text.isEmpty else {
                        errorMessage = "\(key) expects a number."
                        return
                    }
                    Task { await saveProperty(key: key, value: text) }
                }
            case .tags:
                VStack(alignment: .leading, spacing: 4) {
                    let tags = value.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
                    if !tags.isEmpty {
                        HStack {
                            ForEach(tags, id: \.self) { tag in
                                Button(action: { onTagClick?(tag) }) {
                                    Text(tag)
                                        .font(.caption)
                                        .padding(.horizontal, 6)
                                        .padding(.vertical, 2)
                                        .background(Color.white.opacity(0.08))
                                        .cornerRadius(4)
                                }
                                .buttonStyle(.plain)
                                .help("Filter documents tagged \"\(tag)\"")
                            }
                        }
                    }
                    TagEditField(
                        value: value,
                        existingTags: allTags,
                        onCommit: { text in
                            Task { await saveProperty(key: key, value: text) }
                        }
                    )
                }
            case .relation, .text:
                CommitTextField(value: value, monospaced: false) { text in
                    Task { await saveProperty(key: key, value: text) }
                }
            }
        }
    }

    private func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            properties = try await core.docProps(path: notePath)
        } catch {
            errorMessage = "Failed to load properties: \(error.localizedDescription)"
        }
        inverseRelations = (try? await core.relationsInverse(path: notePath)) ?? []
    }

    private func saveProperty(key: String, value: String) async {
        do {
            try await core.noteEditProperty(path: notePath, key: key, value: value)
            errorMessage = nil
            await load()
            onChanged?()
        } catch {
            errorMessage = "Failed to save \(key): \(error.localizedDescription)"
        }
    }
}

/// Text field that only commits its value on submit or focus loss, so typing
/// does not trigger a frontmatter write per keystroke.
private struct CommitTextField: View {
    let value: String
    let monospaced: Bool
    let onCommit: (String) -> Void

    @State private var text = ""
    @FocusState private var focused: Bool

    var body: some View {
        TextField("Value", text: $text)
            .textFieldStyle(RoundedBorderTextFieldStyle())
            .font(monospaced ? .body.monospaced() : .body)
            .focused($focused)
            .onSubmit { commit() }
            .onChange(of: focused) { _, isFocused in
                if !isFocused { commit() }
            }
            .onChange(of: value) { _, newValue in
                if !focused { text = newValue }
            }
            .onAppear { text = value }
    }

    private func commit() {
        guard text != value else { return }
        onCommit(text)
    }
}

/// Tags property editor with autocomplete: while the user types, existing
/// vault tags matching the last comma-separated token are offered as chips
/// that complete the token (issue #306). Commits on submit or focus loss,
/// exactly like `CommitTextField`.
private struct TagEditField: View {
    let value: String
    let existingTags: [String]
    let onCommit: (String) -> Void

    @State private var text = ""
    @FocusState private var focused: Bool

    /// The token after the last comma — the one being typed right now.
    private var currentToken: String {
        guard let last = text.split(separator: ",", omittingEmptySubsequences: false).last else { return "" }
        return String(last).trimmingCharacters(in: .whitespaces)
    }

    private var suggestions: [String] {
        let token = currentToken.lowercased()
        guard !token.isEmpty else { return [] }
        return existingTags
            .filter { $0.lowercased().contains(token) && $0.lowercased() != token }
            .sorted()
            .prefix(5)
            .map { $0 }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            TextField("Value", text: $text)
                .textFieldStyle(RoundedBorderTextFieldStyle())
                .focused($focused)
                .onSubmit { commit() }
                .onChange(of: focused) { _, isFocused in
                    if !isFocused { commit() }
                }
                .onChange(of: value) { _, newValue in
                    if !focused { text = newValue }
                }
                .onAppear { text = value }

            if focused && !suggestions.isEmpty {
                HStack(spacing: 6) {
                    ForEach(suggestions, id: \.self) { tag in
                        Button {
                            completeToken(with: tag)
                        } label: {
                            Text(tag)
                                .font(.caption2)
                                .padding(.horizontal, 6)
                                .padding(.vertical, 2)
                                .background(SymairaTheme.goldPrimary.opacity(0.18))
                                .cornerRadius(4)
                        }
                        .buttonStyle(.plain)
                        .help("Add tag \"\(tag)\"")
                    }
                }
                .transition(.opacity)
            }
        }
    }

    private func completeToken(with tag: String) {
        var components = text.split(separator: ",", omittingEmptySubsequences: false).map(String.init)
        if components.isEmpty {
            components = [tag]
        } else {
            components[components.count - 1] = tag
        }
        text = components.joined(separator: ", ")
    }

    private func commit() {
        guard text != value else { return }
        onCommit(text)
    }
}
