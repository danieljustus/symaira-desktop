#if os(macOS)
import Foundation
import SymairaToolKit

/// Locates the `symdesk` core binary on this machine.
///
/// `BinaryLocator`'s strict search rejects any `PATH` or Homebrew-prefix
/// directory that is group- or world-writable. Homebrew's Apple Silicon
/// prefix (`/opt/homebrew/bin`) is group-writable by design, so a core
/// installed the documented way — `brew install danieljustus/tap/symdesk` —
/// is invisible to the strict search and the app cannot start at all (#437).
///
/// The strict search is still preferred wherever it succeeds. Only when it
/// finds nothing does a relaxed search run, and the relaxation is recorded
/// rather than applied silently.
enum CoreBinaryDiscovery {
    /// A located core binary, plus why a relaxed search was needed.
    struct Detection {
        let tool: DetectedTool

        /// Non-nil when the strict search found nothing and the relaxed
        /// search succeeded. Names the accepted directory and the reason it
        /// failed the strict check, so the app can report where its core came
        /// from instead of relaxing provenance silently.
        let provenanceNote: String?
    }

    /// Returns the core binary, preferring `strict` and falling back to
    /// `relaxed`, or `nil` when neither finds it — which is the genuine
    /// "not installed" case.
    static func detect(
        _ tool: SymairaTool,
        strict: ToolDetector,
        relaxed: ToolDetector
    ) async -> Detection? {
        if let strictHit = await strict.detect(tool) {
            return Detection(tool: strictHit, provenanceNote: nil)
        }

        guard let relaxedHit = await relaxed.detect(tool) else { return nil }

        let directory = relaxedHit.location.url.deletingLastPathComponent().path
        return Detection(
            tool: relaxedHit,
            provenanceNote: "Loaded \(tool.binaryName) from \(directory). "
                + "That directory is group- or world-writable, so it did not pass the strict provenance check."
        )
    }
}
#endif
