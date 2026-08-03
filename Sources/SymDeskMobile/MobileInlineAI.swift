import Foundation

/// Inline-AI intents, mirroring the desktop `group: inline-ai` contract:
/// the same intent names (`summarize | rewrite | continue`, internal/ai)
/// and the same result semantics. The wire value is the desktop spelling;
/// the display name is user-facing only.
enum MobileInlineAIIntent: String, CaseIterable, Identifiable, Sendable {
    case summarize
    case rewrite
    case `continue`

    var id: String { rawValue }

    /// Value the server endpoint (`POST /api/v1/ai/transform`) and the
    /// desktop `group: inline-ai` understand.
    var desktopValue: String {
        switch self {
        case .summarize: return "summarize"
        case .rewrite: return "rewrite"
        case .continue: return "continue"
        }
    }

    var displayName: String {
        switch self {
        case .summarize: return "Summarise"
        case .rewrite: return "Rewrite"
        case .continue: return "Continue"
        }
    }

    var systemImage: String {
        switch self {
        case .summarize: return "text.quote"
        case .rewrite: return "arrow.triangle.2.circlepath"
        case .continue: return "text.append"
        }
    }

    /// Instruction for the on-device provider (small model, concise).
    var onDeviceInstruction: String {
        switch self {
        case .summarize:
            return "Summarise the text below in a few sentences, in the language of the text."
        case .rewrite:
            return "Rewrite the text below to be clearer and better structured, keeping the meaning and the language."
        case .continue:
            return "Continue the text below naturally from where it ends, in the same language and style."
        }
    }
}

/// Pure text surgery used by the accept path: a transform result replaces
/// the selected range, or the whole text when no (valid) selection exists.
/// Nothing is written anywhere until the user explicitly accepts.
enum MobileInlineAIText {
    static func apply(suggestion: String, to original: String, replacing range: NSRange?) -> String {
        guard let range, range.length > 0, range.location + range.length <= (original as NSString).length else {
            return suggestion
        }
        let source = original as NSString
        return source.replacingCharacters(in: range, with: suggestion)
    }
}

/// Streams a transform through the same provider selection as chat:
/// the server wins when a connection is configured, otherwise (or when
/// the server fails) the on-device provider answers — with the active
/// provider reported back so the UI can show it. MainActor-bound: its
/// provider closures read vault state.
@MainActor
struct MobileInlineAIRunner {
    /// Primary provider (server when configured).
    var primary: () -> MobileAIProvider?
    /// On-device provider to fall back to; nil when the device has none.
    var onDeviceFallback: () -> MobileAIProvider?

    /// Returns the accumulated answer and the display name of the
    /// provider that actually answered.
    func run(
        intent: MobileInlineAIIntent,
        text: String,
        onEvent: @escaping @MainActor @Sendable (MobileAIEvent) -> Void
    ) async throws -> (text: String, providerName: String) {
        guard let provider = primary() else {
            throw MobileAIClient.AIError.notConfigured
        }
        do {
            let answer = try await stream(provider: provider, intent: intent, text: text, onEvent: onEvent)
            return (answer, provider.displayName)
        } catch {
            // Mid-session connectivity loss (or any server failure): fall
            // back to the device automatically, no manual switch.
            guard !provider.isOnDevice, let onDevice = onDeviceFallback() else {
                throw error
            }
            let answer = try await stream(provider: onDevice, intent: intent, text: text, onEvent: onEvent)
            return (answer, onDevice.displayName)
        }
    }

    private func stream(
        provider: MobileAIProvider,
        intent: MobileInlineAIIntent,
        text: String,
        onEvent: @escaping @MainActor @Sendable (MobileAIEvent) -> Void
    ) async throws -> String {
        let accumulator = TextAccumulator()
        try await provider.transform(text: text, intent: intent.desktopValue) { event in
            if event.type == .answer, let chunk = event.text {
                accumulator.append(chunk)
            }
            // Hop back to the main actor for the UI callback.
            Task { @MainActor in
                onEvent(event)
            }
        }
        return accumulator.snapshot()
    }
}

/// Thread-safe string accumulator for streaming answers (the provider
/// delivers events on its own queue).
private final class TextAccumulator: @unchecked Sendable {
    private let lock = NSLock()
    private var value = ""

    func append(_ chunk: String) {
        lock.lock()
        value += chunk
        lock.unlock()
    }

    func snapshot() -> String {
        lock.lock()
        defer { lock.unlock() }
        return value
    }
}

/// View model for the inline-AI sheet. The single write path is
/// `accept` — streaming alone never writes anything; `undo` restores the
/// original text through the same write path.
@MainActor
final class MobileInlineAIModel: ObservableObject {
    enum Phase: Equatable {
        case idle
        case streaming
        case done
        case failed
    }

    @Published private(set) var phase: Phase = .idle
    @Published private(set) var suggestion = ""
    @Published private(set) var activeProviderName: String?
    @Published private(set) var isOnDevice = false
    @Published private(set) var capabilityNote: String?
    @Published private(set) var accepted = false
    @Published private(set) var canUndo = false
    @Published var errorMessage: String?

    /// The exact text the last transform ran on (the "Original" side).
    private(set) var transformedSource = ""

    private let original: String
    private let runner: MobileInlineAIRunner
    private let save: (String) async throws -> Void
    private var task: Task<Void, Never>?

    init(
        original: String,
        runner: MobileInlineAIRunner,
        save: @escaping (String) async throws -> Void
    ) {
        self.original = original
        self.runner = runner
        self.save = save
    }

    var hasResult: Bool { !suggestion.isEmpty }

    func start(intent: MobileInlineAIIntent, text: String, selectedRange: NSRange?) {
        task?.cancel()
        phase = .streaming
        suggestion = ""
        errorMessage = nil
        accepted = false
        canUndo = false
        transformedSource = text

        let providerHint = runner.primary()?.displayName
        activeProviderName = providerHint
        isOnDevice = runner.primary()?.isOnDevice ?? false
        capabilityNote = providerHint == "On-device"
            ? "On-device model — answers are shorter and weaker than the server model."
            : nil

        task = Task { @MainActor [runner] in
            do {
                let result = try await runner.run(intent: intent, text: text) { @MainActor event in
                    if event.type == .answer, let chunk = event.text {
                        self.suggestion += chunk
                    }
                }
                // The runner reports the provider that actually answered
                // (server, or on-device after a fallback).
                self.activeProviderName = result.providerName
                self.isOnDevice = result.providerName == "On-device"
                self.capabilityNote = result.providerName == "On-device"
                    ? "On-device model — answers are shorter and weaker than the server model."
                    : nil
                if result.text.isEmpty {
                    self.suggestion = "_No answer received._"
                }
                self.phase = .done
            } catch is CancellationError {
                self.phase = .idle
            } catch {
                self.errorMessage = error.localizedDescription
                self.phase = .failed
            }
        }
    }

    func cancel() {
        task?.cancel()
        task = nil
        if phase == .streaming { phase = .idle }
    }

    /// Discards the current suggestion without writing anything.
    func discardResult() {
        task?.cancel()
        task = nil
        suggestion = ""
        errorMessage = nil
        if phase != .done { phase = .idle }
    }

    /// The ONLY write path: merges the suggestion into the current text
    /// (replacing the selection when one was active) and queues it through
    /// the write layer. Never called implicitly.
    func accept(currentText: String, selectedRange: NSRange?) async {
        guard !suggestion.isEmpty, !accepted else { return }
        do {
            let merged = MobileInlineAIText.apply(
                suggestion: suggestion,
                to: currentText,
                replacing: selectedRange
            )
            try await save(merged)
            accepted = true
            canUndo = true
            errorMessage = nil
        } catch {
            errorMessage = "Could not save: \(error.localizedDescription)"
        }
    }

    /// Restores the text as it was before the AI session, through the same
    /// write path (precondition checks still apply).
    func undo() async {
        guard canUndo else { return }
        do {
            try await save(original)
            canUndo = false
            accepted = false
            suggestion = ""
            phase = .idle
            errorMessage = nil
        } catch {
            errorMessage = "Could not undo: \(error.localizedDescription)"
        }
    }
}
