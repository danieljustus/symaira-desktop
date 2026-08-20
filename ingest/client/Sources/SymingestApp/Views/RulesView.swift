import SwiftUI

struct RulesView: View {
    @Environment(ConfigStore.self) private var configStore
    
    @State private var rules: [SwiftRule] = []
    @State private var isLoading = false
    @State private var errorMessage: String?
    
    // Form state
    @State private var newPattern = ""
    @State private var newKind = "category"
    @State private var newValue = ""
    @State private var isAdding = false
    @State private var editingRule: SwiftRule?
    @State private var editPattern = ""
    @State private var editKind = "category"
    @State private var editValue = ""
    @State private var testText = ""
    @State private var testResult: String?
    
    let ruleKinds = [
        ("Category", "category"),
        ("Tag", "tag"),
        ("Correspondent", "correspondent"),
        ("Document Type", "document_type")
    ]
    
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            // Header
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Classification Rules")
                        .font(Font.title.bold())
                        .foregroundStyle(Theme.textPrimary)
                    Text("Define rules to automatically tag or categorize documents based on matching text substrings.")
                        .font(.subheadline)
                        .foregroundStyle(Theme.textSecondary)
                }
                Spacer()
                
                Button(action: { Task { await loadRules() } }) {
                    Label("Refresh", systemImage: "arrow.clockwise")
                }
                .disabled(isLoading)
                .buttonStyle(.bordered)
            }
            
            // Inline Add Rule Form
            VStack(alignment: .leading, spacing: 10) {
                Text("Add New Rule")
                    .font(.headline)
                    .foregroundStyle(Theme.goldPrimary)
                
                HStack(spacing: 12) {
                    // Pattern
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Pattern (Text Substring)")
                            .font(.caption)
                            .foregroundStyle(Theme.textSecondary)
                        TextField("e.g. Acme Corp, Invoice", text: $newPattern)
                            .textFieldStyle(.roundedBorder)
                    }
                    
                    // Kind
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Type")
                            .font(.caption)
                            .foregroundStyle(Theme.textSecondary)
                        Picker("", selection: $newKind) {
                            ForEach(ruleKinds, id: \.1) { item in
                                Text(item.0).tag(item.1)
                            }
                        }
                        .pickerStyle(.menu)
                        .labelsHidden()
                        .frame(width: 150)
                    }
                    
                    // Value
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Assigned Value")
                            .font(.caption)
                            .foregroundStyle(Theme.textSecondary)
                        TextField("e.g. Utilities, Invoice", text: $newValue)
                            .textFieldStyle(.roundedBorder)
                    }
                    
                    // Submit button
                    VStack(alignment: .leading, spacing: 4) {
                        Text(" ") // Spacer for alignment
                            .font(.caption)
                        Button {
                            Task {
                                await addRule()
                            }
                        } label: {
                            if isAdding {
                                ProgressView()
                                    .controlSize(.small)
                            } else {
                                Label("Add Rule", systemImage: "plus")
                                    .foregroundStyle(Theme.bgDark)
                            }
                        }
                        .buttonStyle(.borderedProminent)
                        .tint(Theme.goldPrimary)
                        .disabled(newPattern.isEmpty || newValue.isEmpty || isAdding)
                    }
                }
            }
            .padding()
            .background(Theme.bgCard)
            .clipShape(RoundedRectangle(cornerRadius: 10))
            .overlay(
                RoundedRectangle(cornerRadius: 10)
                    .stroke(Theme.borderGlass, lineWidth: 1)
            )

            // Test rules and edit selected rule
            VStack(alignment: .leading, spacing: 10) {
                Text("Test / Edit Rules")
                    .font(.headline)
                    .foregroundStyle(Theme.goldPrimary)
                HStack {
                    TextField("Paste sample extracted text to test matching rules", text: $testText)
                        .textFieldStyle(.roundedBorder)
                    Button("Test") {
                        Task { await testRules() }
                    }
                    .buttonStyle(.bordered)
                    .disabled(testText.isEmpty)
                }
                if let testResult {
                    Text(testResult)
                        .font(.caption.monospaced())
                        .foregroundStyle(Theme.textSecondary)
                        .lineLimit(4)
                }
                if let editingRule {
                    Divider().background(Theme.borderGlass)
                    Text("Editing rule #\(editingRule.id)")
                        .font(.caption.bold())
                        .foregroundStyle(Theme.textSecondary)
                    HStack {
                        TextField("Pattern", text: $editPattern).textFieldStyle(.roundedBorder)
                        Picker("", selection: $editKind) {
                            ForEach(ruleKinds, id: \.1) { item in Text(item.0).tag(item.1) }
                        }
                        .pickerStyle(.menu)
                        .frame(width: 150)
                        TextField("Value", text: $editValue).textFieldStyle(.roundedBorder)
                        Button("Save") {
                            Task { await updateRule(id: editingRule.id) }
                        }
                        .buttonStyle(.borderedProminent)
                        .tint(Theme.goldPrimary)
                        Button("Cancel") { self.editingRule = nil }
                            .buttonStyle(.bordered)
                    }
                }
            }
            .padding()
            .background(Theme.bgCard)
            .clipShape(RoundedRectangle(cornerRadius: 10))
            .overlay(
                RoundedRectangle(cornerRadius: 10)
                    .stroke(Theme.borderGlass, lineWidth: 1)
            )
            
            // Error Message
            if let error = errorMessage {
                Text(error)
                    .foregroundStyle(.red)
                    .font(.caption)
                    .padding()
                    .background(Color.red.opacity(0.1))
                    .clipShape(RoundedRectangle(cornerRadius: 6))
            }
            
            // Rules list
            if isLoading && rules.isEmpty {
                VStack {
                    Spacer()
                    ProgressView("Loading classification rules...")
                    Spacer()
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if rules.isEmpty {
                VStack {
                    Spacer()
                    ContentUnavailableView(
                        "No rules configured",
                        systemImage: "tag.slash",
                        description: Text("Use the form above to add your first classification rule.")
                    )
                    Spacer()
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                List {
                    // Header
                    HStack {
                        Text("ID").frame(width: 40, alignment: .leading)
                        Text("Pattern Substring").frame(maxWidth: .infinity, alignment: .leading)
                        Text("Type").frame(width: 150, alignment: .leading)
                        Text("Assigned Value").frame(width: 180, alignment: .leading)
                        Text("Action").frame(width: 120, alignment: .trailing)
                    }
                    .font(.subheadline.bold())
                    .foregroundStyle(Theme.textSecondary)
                    .padding(.vertical, 6)
                    .listRowBackground(Color.clear)
                    
                    Divider()
                        .listRowBackground(Color.clear)
                    
                    ForEach(rules) { rule in
                        HStack {
                            Text("\(rule.id)")
                                .font(.caption.monospaced())
                                .foregroundStyle(Theme.textMuted)
                                .frame(width: 40, alignment: .leading)
                            
                            Text(rule.pattern)
                                .foregroundStyle(Theme.textPrimary)
                                .fontWeight(.medium)
                                .frame(maxWidth: .infinity, alignment: .leading)
                            
                            // Kind Badge
                            Text(rule.kind.replacingOccurrences(of: "_", with: " ").capitalized)
                                .font(.caption.monospaced())
                                .padding(.horizontal, 8)
                                .padding(.vertical, 3)
                                .background(Theme.iceSecondary.opacity(0.12))
                                .foregroundStyle(Theme.icePrimary)
                                .clipShape(Capsule())
                                .frame(width: 150, alignment: .leading)
                            
                            Text(rule.value)
                                .foregroundStyle(Theme.goldSecondary)
                                .frame(width: 180, alignment: .leading)
                            
                            HStack(spacing: 10) {
                                Button("Edit") {
                                    startEditing(rule)
                                }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                                Button(role: .destructive) {
                                    Task {
                                        await deleteRule(id: rule.id)
                                    }
                                } label: {
                                    Image(systemName: "trash")
                                        .foregroundStyle(.red)
                                }
                                .buttonStyle(.plain)
                            }
                            .frame(width: 120, alignment: .trailing)
                        }
                        .padding(.vertical, 8)
                        .listRowBackground(Theme.bgCard)
                    }
                }
                .listStyle(.plain)
                .background(Color.clear)
            }
        }
        .onAppear {
            Task {
                await loadRules()
            }
        }
    }
    
    private func loadRules() async {
        isLoading = true
        errorMessage = nil
        do {
            self.rules = try await CLIClient.shared.listRules(config: configStore)
        } catch {
            errorMessage = "Failed to load rules: \(error.localizedDescription)"
        }
        isLoading = false
    }
    
    private func addRule() async {
        isAdding = true
        errorMessage = nil
        let (success, message) = await CLIClient.shared.addRule(
            pattern: newPattern,
            kind: newKind,
            value: newValue,
            config: configStore
        )
        isAdding = false
        if success {
            newPattern = ""
            newValue = ""
            await loadRules()
        } else {
            errorMessage = "Failed to add rule: \(message)"
        }
    }

    private func startEditing(_ rule: SwiftRule) {
        editingRule = rule
        editPattern = rule.pattern
        editKind = rule.kind
        editValue = rule.value
    }

    private func updateRule(id: Int64) async {
        errorMessage = nil
        let (success, message) = await CLIClient.shared.updateRule(id: id, pattern: editPattern, kind: editKind, value: editValue, config: configStore)
        if success {
            editingRule = nil
            await loadRules()
        } else {
            errorMessage = "Failed to update rule: \(message)"
        }
    }

    private func testRules() async {
        errorMessage = nil
        let (success, message) = await CLIClient.shared.testRules(text: testText, config: configStore)
        if success {
            testResult = message.isEmpty ? "No output" : message
        } else {
            errorMessage = "Rule test failed: \(message)"
        }
    }
    
    private func deleteRule(id: Int64) async {
        errorMessage = nil
        let (success, message) = await CLIClient.shared.deleteRule(id: id, config: configStore)
        if success {
            await loadRules()
        } else {
            errorMessage = "Failed to delete rule: \(message)"
        }
    }
}
