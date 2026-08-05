import SwiftUI

/// In-app AI configuration: provider, model and endpoint are visible and
/// configurable here — never only an environment variable the app cannot
/// set (the #305 mistake). The pane states plainly that vault content
/// leaves the device when a remote provider is used, before any request.
struct MobileAISettingsView: View {
    @EnvironmentObject private var vault: MobileVaultStore
    @Environment(\.dismiss) private var dismiss

    @State private var provider: String
    @State private var model: String
    @State private var endpoint: String
    @State private var hasUnsavedChanges = false

    init() {
        let config = MobileAISettings.load()
        _provider = State(initialValue: config.provider)
        _model = State(initialValue: config.model)
        _endpoint = State(initialValue: config.endpoint)
    }

    var body: some View {
        NavigationStack {
            MobileBackdrop {
                ScrollView {
                    VStack(alignment: .leading, spacing: 18) {
                        privacyCard

                        VStack(alignment: .leading, spacing: 14) {
                            Label("Provider", systemImage: "cpu")
                                .font(.headline)
                                .foregroundStyle(MobileTheme.textPrimary)

                            Picker("Provider", selection: $provider) {
                                Text("Self-hosted server").tag("server")
                                Text("Anthropic").tag("anthropic")
                                Text("Ollama").tag("ollama")
                            }
                            .pickerStyle(.segmented)

                            if provider == "server" {
                                Text("Uses the server's configured model via \(vault.serverURL?.host ?? "your server"). The server may forward excerpts to its own provider — see your server's AI settings.")
                                    .font(.caption)
                                    .foregroundStyle(MobileTheme.textSecondary)
                            }

                            if provider != "server" {
                                TextField("Model (e.g. claude-sonnet-4-5)", text: $model)
                                    .textFieldStyle(.roundedBorder)
                                    .textInputAutocapitalization(.never)
                            }

                            TextField("Endpoint (https://…)", text: $endpoint)
                                .textFieldStyle(.roundedBorder)
                                .textInputAutocapitalization(.never)
                                .keyboardType(.URL)
                                .disabled(provider == "server")

                            if provider != "server" && (model.isEmpty || MobileServerConfig.normalizedURL(endpoint) == nil) {
                                Label("Enter a model and a valid endpoint to enable direct provider mode.", systemImage: "info.circle")
                                    .font(.caption)
                                    .foregroundStyle(MobileTheme.goldSoft)
                            }
                        }
                        .padding(18)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .mobileLiquidGlass(elevated: true)

                        if hasUnsavedChanges {
                            Button {
                                save()
                            } label: {
                                Label("Save AI settings", systemImage: "checkmark.circle.fill")
                                    .frame(maxWidth: .infinity)
                            }
                            .buttonStyle(.borderedProminent)
                            .tint(MobileTheme.gold)
                            .foregroundStyle(.black)
                        }
                    }
                    .padding(16)
                    .frame(maxWidth: 680)
                    .frame(maxWidth: .infinity)
                }
            }
            .navigationTitle("AI settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .onChange(of: provider) { _, _ in hasUnsavedChanges = true }
            .onChange(of: model) { _, _ in hasUnsavedChanges = true }
            .onChange(of: endpoint) { _, _ in hasUnsavedChanges = true }
        }
    }

    private var privacyCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label("Privacy", systemImage: "hand.raised.fill")
                .font(.headline)
                .foregroundStyle(MobileTheme.textPrimary)
            Text("Answers require sending your question and the relevant vault excerpts to the configured AI provider. With “Self-hosted server”, content goes to your server, which forwards it to its configured model. Vault content leaves this device whenever the provider is remote.")
                .font(.subheadline)
                .foregroundStyle(MobileTheme.textSecondary)
        }
        .padding(18)
        .frame(maxWidth: .infinity, alignment: .leading)
        .mobileLiquidGlass(cornerRadius: 18)
    }

    private func save() {
        let defaults = UserDefaults.standard
        defaults.set(provider, forKey: MobileAISettings.providerKey)
        defaults.set(model, forKey: MobileAISettings.modelKey)
        defaults.set(endpoint, forKey: MobileAISettings.endpointKey)
        hasUnsavedChanges = false
    }
}
