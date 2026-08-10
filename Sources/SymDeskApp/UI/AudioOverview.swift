import Foundation
import AVFoundation
import SymDeskCore

/// Errors surfaced by Audio Overview generation (issue #429). Every case
/// maps to a plain-language message the studio pane shows directly — this
/// feature must degrade honestly, not silently, wherever the system
/// synthesizer or narration text is unavailable.
enum AudioOverviewError: LocalizedError {
    case synthesisUnavailable
    case emptyNarration
    case writeFailed(String)

    var errorDescription: String? {
        switch self {
        case .synthesisUnavailable:
            return "No system speech synthesizer is available on this Mac. The written artifact is still available in the Studio pane."
        case .emptyNarration:
            return "There is nothing to narrate yet — generate a written artifact first."
        case .writeFailed(let reason):
            return "Could not generate the Audio Overview: \(reason)"
        }
    }
}

/// Turns a generated studio artifact (issue #426) into a narration script
/// and synthesizes it locally via the macOS system speech synthesizer
/// (issue #429). Narration only, no cloud TTS and no bundled neural model —
/// see the issue for why: local-first, and macOS already ships a capable
/// synthesizer.
enum AudioOverview {
    private static let frontmatterFence = "---"
    private static let wikilinkPattern = try! NSRegularExpression(pattern: #"\[\[([^\]|]+)(?:\|([^\]]+))?\]\]"#)
    private static let markdownLinkPattern = try! NSRegularExpression(pattern: #"\[([^\]]*)\]\([^)]*\)"#)
    private static let codeFencePattern = try! NSRegularExpression(pattern: #"```[\s\S]*?```"#)
    private static let inlineCodePattern = try! NSRegularExpression(pattern: #"`([^`]*)`"#)
    private static let headingPattern = try! NSRegularExpression(pattern: #"(?m)^#{1,6}\s+"#)
    private static let emphasisPattern = try! NSRegularExpression(pattern: #"(\*{1,3}|_{1,3})([^*_]+)\1"#)
    private static let blankLinesPattern = try! NSRegularExpression(pattern: #"\n{3,}"#)

    /// Strips frontmatter, wikilink syntax and Markdown formatting from
    /// `content` into narration-safe prose, then appends a spoken
    /// source-attribution sentence built from `sources` (vault-relative
    /// paths). The result contains no raw Markdown or wikilink syntax
    /// (issue #429 acceptance criteria).
    static func narrationText(fromArtifactContent content: String, sources: [String]) -> String {
        var body = stripFrontmatter(content)
        body = replace(codeFencePattern, in: body, template: " ")
        body = replace(inlineCodePattern, in: body, template: "$1")
        body = replace(wikilinkPattern, in: body) { groups in
            // groups[0] = whole match, groups[1] = target, groups[2] = display
            // alias (may be absent). Prefer the alias when present.
            groups.count > 2 && !groups[2].isEmpty ? groups[2] : groups[1]
        }
        body = replace(markdownLinkPattern, in: body, template: "$1")
        body = replace(headingPattern, in: body, template: "")
        body = replace(emphasisPattern, in: body, template: "$2")
        body = body.replacingOccurrences(of: "#", with: "")
        body = replace(blankLinesPattern, in: body, template: "\n\n")
        body = body.trimmingCharacters(in: .whitespacesAndNewlines)

        let names = sources.map { ($0 as NSString).lastPathComponent }
        if !names.isEmpty {
            let attribution = names.count == 1
                ? "This overview is based on \(names[0])."
                : "This overview is based on: \(names.joined(separator: ", "))."
            body = body.isEmpty ? attribution : body + "\n\n" + attribution
        }
        return body
    }

    private static func stripFrontmatter(_ content: String) -> String {
        guard content.hasPrefix(frontmatterFence) else { return content }
        let lines = content.components(separatedBy: "\n")
        guard lines.first?.trimmingCharacters(in: .whitespaces) == frontmatterFence else { return content }
        for (index, line) in lines.enumerated() where index > 0 && line.trimmingCharacters(in: .whitespaces) == frontmatterFence {
            return lines[(index + 1)...].joined(separator: "\n")
        }
        return content
    }

    private static func replace(_ regex: NSRegularExpression, in text: String, template: String) -> String {
        let range = NSRange(text.startIndex..., in: text)
        return regex.stringByReplacingMatches(in: text, range: range, withTemplate: template)
    }

    /// Applies `transform` to each match's captured groups (group 0 is the
    /// whole match), replacing it with the returned string. Used for the
    /// wikilink case, where the replacement depends on whether a display
    /// alias (`[[target|display]]`) was present.
    private static func replace(_ regex: NSRegularExpression, in text: String, transform: ([String]) -> String) -> String {
        let nsText = text as NSString
        let matches = regex.matches(in: text, range: NSRange(location: 0, length: nsText.length))
        guard !matches.isEmpty else { return text }

        var result = ""
        var lastEnd = 0
        for match in matches {
            result += nsText.substring(with: NSRange(location: lastEnd, length: match.range.location - lastEnd))
            var groups: [String] = []
            for i in 0..<match.numberOfRanges {
                let r = match.range(at: i)
                groups.append(r.location == NSNotFound ? "" : nsText.substring(with: r))
            }
            result += transform(groups)
            lastEnd = match.range.location + match.range.length
        }
        result += nsText.substring(from: lastEnd)
        return result
    }

    /// Synthesizes `text` to an audio file at `outputURL` using the system
    /// speech synthesizer. Throws `.emptyNarration` for blank text and
    /// `.synthesisUnavailable` when the system has no usable voice.
    static func synthesize(text: String, voiceIdentifier: String?, rate: Float, to outputURL: URL) async throws {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { throw AudioOverviewError.emptyNarration }

        let voice = voiceIdentifier.flatMap { AVSpeechSynthesisVoice(identifier: $0) }
            ?? AVSpeechSynthesisVoice(language: Locale.current.identifier)
            ?? AVSpeechSynthesisVoice.speechVoices().first
        guard let voice else { throw AudioOverviewError.synthesisUnavailable }

        let utterance = AVSpeechUtterance(string: trimmed)
        utterance.voice = voice
        utterance.rate = rate

        let synthesizer = AVSpeechSynthesizer()
        final class WriteState: @unchecked Sendable {
            var audioFile: AVAudioFile?
            var resumed = false
        }
        let state = WriteState()

        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
            synthesizer.write(utterance) { buffer in
                guard let pcmBuffer = buffer as? AVAudioPCMBuffer else { return }
                if state.resumed { return }
                if pcmBuffer.frameLength == 0 {
                    state.resumed = true
                    continuation.resume()
                    return
                }
                do {
                    if state.audioFile == nil {
                        state.audioFile = try AVAudioFile(forWriting: outputURL, settings: pcmBuffer.format.settings)
                    }
                    try state.audioFile?.write(from: pcmBuffer)
                } catch {
                    state.resumed = true
                    continuation.resume(throwing: AudioOverviewError.writeFailed(error.localizedDescription))
                }
            }
        }
    }

    /// Available system voices, sorted for a friendly picker.
    static func availableVoices() -> [AVSpeechSynthesisVoice] {
        AVSpeechSynthesisVoice.speechVoices().sorted { $0.name < $1.name }
    }

    /// Generates and stores an Audio Overview for one notebook artifact:
    /// synthesizes to a temp file, then moves it into the vault via the
    /// same `VaultAssets` convention every other binary attachment in this
    /// app uses (see MarkdownEditorView.storeImageAsset for the identical
    /// pasted-image precedent). Local-vault only — `vaultRoot` is nil in
    /// server-connected mode, where there is no local filesystem to write
    /// into; that case fails with a plain-language message rather than a
    /// silent no-op, matching the image-paste precedent exactly.
    static func generateAndStore(
        artifactContent: String,
        sources: [String],
        notebookID: String,
        vaultRoot: String?,
        voiceIdentifier: String?,
        rate: Float
    ) async throws -> String {
        guard let vaultRoot else {
            throw AudioOverviewError.writeFailed(
                "Audio Overview requires a local vault. Connect to a vault or open a folder to generate audio.")
        }
        let narration = narrationText(fromArtifactContent: artifactContent, sources: sources)
        let tempURL = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".caf")
        defer { try? FileManager.default.removeItem(at: tempURL) }

        try await synthesize(text: narration, voiceIdentifier: voiceIdentifier, rate: rate, to: tempURL)

        let data: Data
        do {
            data = try Data(contentsOf: tempURL)
        } catch {
            throw AudioOverviewError.writeFailed(error.localizedDescription)
        }
        do {
            return try VaultAssets.store(
                imageData: data,
                preferredName: "\(notebookID)-audio-overview",
                fileExtension: "caf",
                vaultRoot: vaultRoot
            )
        } catch {
            throw AudioOverviewError.writeFailed(error.localizedDescription)
        }
    }
}
