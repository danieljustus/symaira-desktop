import Foundation
import FoundationModels

/// On-device provider: grounds the query in the local vault snapshot and
/// answers with the device's foundation model where available. No vault
/// content leaves the device while this provider is active.
///
/// The model backend is injectable so unit tests can exercise the whole
/// prompt/grounding/citation pipeline without a physical device.
struct MobileOnDeviceAIProvider: MobileAIProvider {
    var displayName: String { "On-device" }
    var isOnDevice: Bool { true }

    /// Whether this device can run the on-device model.
    let isAvailable: Bool

    private let retriever: MobileLocalRetriever
    private let model: MobileOnDeviceModelProtocol
    /// Capability note surfaced with every answer so users do not mistake
    /// a small local model for server parity.
    let capabilityNote: String

    init(vaultNotes: [MobileNote], model: MobileOnDeviceModelProtocol? = nil) {
        self.retriever = MobileLocalRetriever(notes: vaultNotes)
        let resolvedModel = model ?? MobileOnDeviceModel()
        self.model = resolvedModel
        self.isAvailable = resolvedModel.isAvailable
        self.capabilityNote = resolvedModel.capabilityNote
    }

    func ask(query: String, onEvent: @escaping @Sendable (MobileAIEvent) -> Void) async throws {
        guard isAvailable else {
            throw MobileAIClient.AIError.notConfigured
        }
        // 1) Tool event: retrieval.
        onEvent(MobileAIEvent(type: .tool, text: nil, path: nil, title: nil, snippet: nil, score: nil, toolName: "search", status: "running"))
        let context = retriever.retrieve(query: query, limit: 4)
        onEvent(MobileAIEvent(type: .tool, text: nil, path: nil, title: nil, snippet: nil, score: nil, toolName: "search", status: "done"))

        // 2) Citations from the grounded context.
        for doc in context {
            onEvent(MobileAIEvent(
                type: .citation,
                text: nil,
                path: doc.path,
                title: doc.title,
                snippet: doc.snippet,
                score: doc.score,
                toolName: nil,
                status: nil
            ))
        }

        // 3) One prompt-construction path for both providers; the
        //    on-device variant is told to be concise.
        let prompt = MobileAIPromptBuilder.build(query: query, context: context, onDevice: true)

        // 4) Answer from the device model (streamed as one event; the
        //    model API returns the full response).
        let answer = try await model.respond(to: prompt)
        onEvent(MobileAIEvent(type: .answer, text: answer, path: nil, title: nil, snippet: nil, score: nil, toolName: nil, status: nil))
        onEvent(MobileAIEvent(type: .done, text: nil, path: nil, title: nil, snippet: nil, score: nil, toolName: nil, status: nil))
    }

    /// Intent-based transformation entirely on the device: the provided
    /// text is transformed (summarize | rewrite | continue) without any
    /// retrieval — like the server's transform endpoint, the vault is
    /// never touched. No content leaves the device.
    func transform(text: String, intent: String, onEvent: @escaping @Sendable (MobileAIEvent) -> Void) async throws {
        guard isAvailable else {
            throw MobileAIClient.AIError.notConfigured
        }
        let instruction = MobileOnDeviceTransformInstruction.forIntent(intent)
        let prompt = """
        You are a small on-device model. \(instruction)
        Answer concisely, in the language of the text, and output only the transformed text.

        Text:
        \(text)
        """
        onEvent(MobileAIEvent(type: .tool, text: nil, path: nil, title: nil, snippet: nil, score: nil, toolName: "transform", status: "running"))
        let answer = try await model.respond(to: prompt)
        onEvent(MobileAIEvent(type: .tool, text: nil, path: nil, title: nil, snippet: nil, score: nil, toolName: "transform", status: "done"))
        onEvent(MobileAIEvent(type: .answer, text: answer, path: nil, title: nil, snippet: nil, score: nil, toolName: nil, status: nil))
        onEvent(MobileAIEvent(type: .done, text: nil, path: nil, title: nil, snippet: nil, score: nil, toolName: nil, status: nil))
    }
}

/// Maps desktop intent values to the on-device instruction. Unknown
/// intents fall back to rewrite, mirroring the server behaviour.
enum MobileOnDeviceTransformInstruction {
    static func forIntent(_ intent: String) -> String {
        switch intent {
        case "summarize":
            return "Summarise the text below in a few sentences, in the language of the text."
        case "continue":
            return "Continue the text below naturally from where it ends, in the same language and style."
        default:
            return "Rewrite the text below to be clearer and better structured, keeping the meaning and the language."
        }
    }
}

/// Shared prompt construction: one path for server and on-device so
/// answers do not diverge; provider-specific limits only (length, tone).
enum MobileAIPromptBuilder {
    static func build(query: String, context: [MobileLocalRetriever.Document], onDevice: Bool) -> String {
        var prompt = ""
        if onDevice {
            prompt += "You are a small on-device model answering from the user's vault. Answer concisely (a few sentences) in the language of the question. If the excerpts do not contain the answer, say so briefly.\n\n"
        } else {
            prompt += "Answer the question using the vault excerpts below. Cite the documents you used.\n\n"
        }
        prompt += "Question: \(query)\n\n"
        prompt += "Vault excerpts:\n"
        if context.isEmpty {
            prompt += "(no matching documents)\n"
        } else {
            for (index, doc) in context.enumerated() {
                prompt += "[\(index + 1)] \(doc.path): \(doc.snippet)\n"
            }
        }
        return prompt
    }
}

/// Local retrieval over the parsed vault snapshot: field-weighted term
/// scoring (title > body), top-k by score. This is the on-device
/// grounding — no network involved.
struct MobileLocalRetriever {
    struct Document: Sendable {
        let path: String
        let title: String
        let snippet: String
        let score: Double
    }

    private let notes: [MobileNote]

    init(notes: [MobileNote]) {
        self.notes = notes
    }

    func retrieve(query: String, limit: Int) -> [Document] {
        let terms = MobileVaultParser.normalizedSearchQuery(query)
            .split(whereSeparator: \.isWhitespace)
            .map(String.init)
            .filter { !$0.isEmpty }
        guard !terms.isEmpty else { return [] }

        let scored = notes.compactMap { note -> Document? in
            let title = MobileVaultParser.normalizedSearchQuery(note.title)
            let body = note.searchText
            var score = 0.0
            for term in terms {
                if title.contains(term) { score += 3 }
                if body.contains(term) { score += 1 }
            }
            guard score > 0 else { return nil }
            let snippet = snippet(for: note, terms: terms)
            return Document(
                path: note.path,
                title: note.title,
                snippet: snippet,
                score: score
            )
        }
        return scored
            .sorted { $0.score > $1.score }
            .prefix(limit)
            .map { $0 }
    }

    private func snippet(for note: MobileNote, terms: [String]) -> String {
        let body = note.body.replacingOccurrences(of: "\n", with: " ")
        let lowered = body.lowercased()
        for term in terms {
            if let range = lowered.range(of: term) {
                // Window of ~60 chars before and ~100 chars after the match.
                let before = body.distance(from: body.startIndex, to: range.lowerBound)
                let start = body.index(range.lowerBound, offsetBy: -min(60, before), limitedBy: body.startIndex) ?? body.startIndex
                let after = body.distance(from: range.upperBound, to: body.endIndex)
                let end = body.index(range.upperBound, offsetBy: min(100, after), limitedBy: body.endIndex) ?? body.endIndex
                let slice = body[start..<end]
                return "…" + slice + "…"
            }
        }
        return String(body.prefix(160))
    }
}

/// Abstraction over the on-device model so tests can inject a fake.
protocol MobileOnDeviceModelProtocol: Sendable {
    var isAvailable: Bool { get }
    var capabilityNote: String { get }
    func respond(to prompt: String) async throws -> String
}

/// Apple's on-device foundation model where available. Uses the
/// FoundationModels framework (iOS 26+ for the language model API);
/// devices without model support or older iOS report
/// `isAvailable == false` instead of crashing.
struct MobileOnDeviceModel: MobileOnDeviceModelProtocol {
    var isAvailable: Bool {
        if #available(iOS 26.0, *) {
            // A session with the default system model fails to construct on
            // devices without on-device model support.
            return (try? LanguageModelSession()) != nil
        }
        return false
    }
    var capabilityNote: String {
        "On-device model — answers are shorter and weaker than the server model."
    }

    func respond(to prompt: String) async throws -> String {
        if #available(iOS 26.0, *) {
            let session = try LanguageModelSession()
            let response = try await session.respond(to: prompt)
            return response.content
        }
        throw MobileAIClient.AIError.notConfigured
    }
}
