import Foundation

/// NDJSON event emitted by the server's streaming AI endpoints
/// (`POST /api/v1/ai/ask` and `/api/v1/ai/transform`). Field names match
/// the Go `ai.AIEvent` wire contract exactly.
struct MobileAIEvent: Decodable, Sendable {
    enum EventType: String, Decodable, Sendable {
        case answer
        case citation
        case tool
        case done
    }

    let type: EventType
    let text: String?
    let path: String?
    let title: String?
    let snippet: String?
    let score: Double?
    let toolName: String?
    let status: String?

    enum CodingKeys: String, CodingKey {
        case type, text, path, title, snippet, score
        case toolName = "tool_name"
        case status
    }
}

/// Streaming client for the server AI endpoints. Consumes NDJSON lines as
/// they arrive and forwards each parsed event; `onEvent` runs on the
/// session's serial queue, so the UI can render tokens progressively.
final class MobileAIClient: @unchecked Sendable {
    enum AIError: LocalizedError {
        case notConfigured
        case invalidResponse
        case server(Int, String)
        case stream(String)

        var errorDescription: String? {
            switch self {
            case .notConfigured:
                return "No server connection. Connect a server in Settings to use AI."
            case .invalidResponse:
                return "The server returned an invalid response."
            case .server(let status, let message):
                return "Server error \(status): \(message)"
            case .stream(let message):
                return message
            }
        }
    }

    private let connection: MobileServerConnection
    private let session: URLSession

    init(connection: MobileServerConnection, session: URLSession = .shared) {
        self.connection = connection
        self.session = session
    }

    /// Streams an ask request. `onEvent` is called for every NDJSON event
    /// in order; the method returns when the stream ends (done event or
    /// connection close). Throws on transport/HTTP errors before any event
    /// was delivered; mid-stream failures surface as a final `.stream`
    /// error via the thrown value after the events already delivered.
    func ask(query: String, onEvent: @escaping @Sendable (MobileAIEvent) -> Void) async throws {
        try await stream(
            path: "/api/v1/ai/ask",
            body: ["query": query],
            onEvent: onEvent
        )
    }

    /// Streams a transform request (summarize | rewrite | continue).
    func transform(text: String, intent: String, onEvent: @escaping @Sendable (MobileAIEvent) -> Void) async throws {
        try await stream(
            path: "/api/v1/ai/transform",
            body: ["text": text, "intent": intent],
            onEvent: onEvent
        )
    }

    private func stream(
        path: String,
        body: [String: String],
        onEvent: @escaping @Sendable (MobileAIEvent) -> Void
    ) async throws {
        let url = connection.url.appendingPathComponent(path)
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("Bearer \(connection.token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("application/x-ndjson", forHTTPHeaderField: "Accept")
        request.httpBody = try JSONEncoder().encode(body)
        request.timeoutInterval = 600

        let (bytes, response) = try await session.bytes(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw AIError.invalidResponse
        }
        guard (200..<300).contains(http.statusCode) else {
            let data = try? await collectRemaining(bytes)
            let message = (try? JSONDecoder().decode(ErrorEnvelope.self, from: data ?? Data()).error)
                ?? "HTTP \(http.statusCode)"
            throw AIError.server(http.statusCode, message)
        }

        let decoder = JSONDecoder()
        for try await line in bytes.lines {
            let trimmed = line.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !trimmed.isEmpty else { continue }
            guard let event = try? decoder.decode(MobileAIEvent.self, from: Data(trimmed.utf8)) else {
                continue
            }
            onEvent(event)
        }
    }

    private func collectRemaining(_ bytes: URLSession.AsyncBytes) async throws -> Data {
        var data = Data()
        for try await byte in bytes {
            data.append(byte)
        }
        return data
    }

    private struct ErrorEnvelope: Decodable { let error: String }
}
