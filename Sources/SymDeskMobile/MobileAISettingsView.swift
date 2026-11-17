import SwiftUI
import SymairaTheme

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
                            Label("Provider", symairaIcon: .cpu)
                                .symairaText(.subheading)

                            SymairaSegmentedControl(
                                "Provider",
                                selection: $provider,
                                options: [
                                    (value: "server", label: "Self-hosted server"),
                                    (value: "anthropic", label: "Anthropic"),
                                    (value: "ollama", label: "Ollama")
                                ]
                            )

                            if provider == "server" {
                                Text("Uses the server's configured model via \(vault.serverURL?.host ?? "your server"). The server may forward excerpts to its own provider — see your server's AI settings.")
                                    .font(SymairaTypography.caption)
                                    .foregroundStyle(SymairaTokens.ink2)
                            }

                            if provider != "server" {
                                TextField("Model (e.g. claude-sonnet-4-5)", text: $model)
                                    .textFieldStyle(.symaira)
                                    .textInputAutocapitalization(.never)
                            }

                            TextField("Endpoint (https://…)", text: $endpoint)
                                .textFieldStyle(.symaira)
                                .textInputAutocapitalization(.never)
                                .keyboardType(.URL)
                                .disabled(provider == "server")

                            if provider != "server" && (model.isEmpty || MobileServerConfig.normalizedURL(endpoint) == nil) {
                                SymairaNotice(message: "Enter a model and a valid endpoint to enable direct provider mode.")
                            }
                        }
                        .padding(18)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .symairaCard(padding: 0)

                        if hasUnsavedChanges {
                            Button {
                                save()
                            } label: {
                                Label("Save AI settings", symairaIcon: .confirm)
                                    .frame(maxWidth: .infinity)
                            }
                            .symairaButtonStyle(.primary)
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
            Label("Privacy", symairaIcon: .shield)
                .symairaText(.subheading)
            Text("Answers require sending your question and the relevant vault excerpts to the configured AI provider. With “Self-hosted server”, content goes to your server, which forwards it to its configured model. Vault content leaves this device whenever the provider is remote.")
                .symairaText(.secondary)
        }
        .padding(18)
        .frame(maxWidth: .infinity, alignment: .leading)
        .symairaCard(padding: 0)
    }

    private func save() {
        let defaults = UserDefaults.standard
        defaults.set(provider, forKey: MobileAISettings.providerKey)
        defaults.set(model, forKey: MobileAISettings.modelKey)
        defaults.set(endpoint, forKey: MobileAISettings.endpointKey)
        hasUnsavedChanges = false
    }
}
