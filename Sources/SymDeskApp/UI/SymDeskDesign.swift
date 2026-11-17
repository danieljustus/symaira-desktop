import SwiftUI
import AppKit
import SymairaTheme
import SymDeskCore

/// Former full-bleed backdrop. The shared `SymairaWorkspace` now paints the
/// canvas (or the topography on the overview), so this only passes content
/// through; existing call sites keep compiling (GUIDELINES MOTION-6: no
/// blueprint grid or ambient glows).
struct SymairaScreen<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View { content }
}

/// AppKit-side equivalents of the Symaira roles for the NSViewRepresentable
/// editors (NSTextView, PDFView). `NSColor(Color)` keeps the adaptive token
/// dynamic, so light and dark appearance both resolve correctly. The legacy
/// member names stay so the editors need no call-site changes.
@MainActor
enum SymairaNSColors {
    /// Opaque reading surface behind editors and viewers (NN-7).
    static var bgDark: NSColor { NSColor(SymairaTokens.surface) }
    static var textPrimary: NSColor { NSColor(SymairaTokens.ink) }
    /// Headings and the insertion point: full-contrast ink, never champagne text (NN-3).
    static var gold: NSColor { NSColor(SymairaTokens.ink) }
    /// Wiki links: secondary ink, underlined by the caller.
    static var goldSecondary: NSColor { NSColor(SymairaTokens.ink2) }
}

/// Tone color for document workflow states. Always shown next to the status
/// text or icon (NN-6): neutral ink for ordinary states, semantic roles only
/// where the meaning demands it.
func symairaStatusColor(_ status: DocumentStatus?) -> Color {
    switch status {
    case .open: return SymairaTokens.ink
    case .paid, .done: return SymairaTokens.positive
    case .submitted: return SymairaTokens.ink2
    case .needsReview: return SymairaTokens.warning
    case .waitingForReply: return SymairaTokens.ink2
    case .none: return SymairaTokens.ink3
    }
}
