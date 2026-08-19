import Foundation

extension DeskCore {
    /// The current AI provider configuration.
    public struct AIConfig: Codable, Sendable, Equatable {
        public let provider: String
        public let ollamaURL: String
        public let model: String
        public let maxTokens: Int
        public let hasAPIKey: Bool

        enum CodingKeys: String, CodingKey {
            case provider
            case ollamaURL = "ollama_url"
            case model
            case maxTokens = "max_tokens"
            case hasAPIKey = "has_api_key"
        }

        public init(provider: String, ollamaURL: String, model: String, maxTokens: Int, hasAPIKey: Bool) {
            self.provider = provider
            self.ollamaURL = ollamaURL
            self.model = model
            self.maxTokens = maxTokens
            self.hasAPIKey = hasAPIKey
        }
    }

    /// The result of testing connectivity to the configured AI provider.
    public struct AIConnectionTestResult: Codable, Sendable, Equatable {
        public let provider: String
        public let ok: Bool
        public let error: String?
        public let models: [String]?

        public init(provider: String, ok: Bool, error: String?, models: [String]?) {
            self.provider = provider
            self.ok = ok
            self.error = error
            self.models = models
        }
    }

    /// Returns the current AI provider configuration.
    public func getAIConfig() async throws -> AIConfig {
        try await runDecoding(AIConfig.self, arguments: ["ai-config", "show", "--json"])
    }

    /// Updates the AI provider configuration. Pass `nil` for any field that
    /// should be left unchanged. Pass an empty string for `apiKey` to clear it.
    public func setAIConfig(
        provider: String? = nil,
        ollamaURL: String? = nil,
        model: String? = nil,
        apiKey: String? = nil,
        maxTokens: Int? = nil
    ) async throws {
        var arguments = ["ai-config", "set"]
        if let provider { arguments += ["--provider", provider] }
        if let ollamaURL { arguments += ["--ollama-url", ollamaURL] }
        if let model { arguments += ["--model", model] }
        if let apiKey { arguments += ["--api-key", apiKey] }
        if let maxTokens { arguments += ["--max-tokens", String(maxTokens)] }
        _ = try await runChecked(arguments: arguments)
    }

    /// Tests connectivity to the configured AI provider and, for Ollama,
    /// returns the list of locally installed models.
    public func testAIConnection() async throws -> AIConnectionTestResult {
        try await runDecoding(AIConnectionTestResult.self, arguments: ["ai-config", "test", "--json"])
    }
}
