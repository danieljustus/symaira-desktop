import Foundation

/// Server-backed provider: wraps `MobileAIClient` unchanged. The server
/// handles retrieval and citations; the phone just streams NDJSON.
struct MobileServerAIProvider: MobileAIProvider {
    let connection: MobileServerConnection
    let client: MobileAIClient

    init(connection: MobileServerConnection, client: MobileAIClient? = nil) {
        self.connection = connection
        self.client = client ?? MobileAIClient(connection: connection)
    }

    var displayName: String { "Server" }
    var isOnDevice: Bool { false }

    func ask(query: String, onEvent: @escaping @Sendable (MobileAIEvent) -> Void) async throws {
        try await client.ask(query: query, onEvent: onEvent)
    }

    func transform(text: String, intent: String, onEvent: @escaping @Sendable (MobileAIEvent) -> Void) async throws {
        try await client.transform(text: text, intent: intent, onEvent: onEvent)
    }
}
