import SwiftUI
import AppKit
import SymairaTheme
import SymDeskCore

/// Full-bleed, low-noise backdrop used behind each main workspace. The grid
/// remains a brand cue, but is deliberately quiet so it never competes with
/// documents or text.
struct SymairaScreen<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        ZStack {
            LinearGradient(
                colors: [SymairaTheme.bgDarker, SymairaTheme.bgDark],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            )
            .ignoresSafeArea()
            BlueprintGrid()
                .opacity(0.35)
                .ignoresSafeArea()
            AmbientGlows()
                .opacity(0.5)
            content
        }
    }
}

/// AppKit-side equivalents of the SymairaTheme tokens for the
/// NSViewRepresentable editors (NSTextView, PDFView) that can't take
/// SwiftUI colors.
@MainActor
enum SymairaNSColors {
    static let bgDark = NSColor(srgbRed: 0x0D / 255, green: 0x0C / 255, blue: 0x0A / 255, alpha: 1)
    static let textPrimary = NSColor(srgbRed: 0xF5 / 255, green: 0xF4 / 255, blue: 0xF0 / 255, alpha: 1)
    static let gold = NSColor(srgbRed: 0xE5 / 255, green: 0xC3 / 255, blue: 0x97 / 255, alpha: 1)
    static let goldSecondary = NSColor(srgbRed: 0xF8 / 255, green: 0xE6 / 255, blue: 0xCD / 255, alpha: 1)
}

/// Brand color for document workflow states: gold for active work,
/// semantic colors only where meaning demands it.
func symairaStatusColor(_ status: DocumentStatus?) -> Color {
    switch status {
    case .open: return SymairaTheme.goldPrimary
    case .paid, .done: return .green
    case .submitted: return SymairaTheme.iceSecondary
    case .needsReview: return .orange
    case .waitingForReply: return SymairaTheme.goldSecondary
    case .none: return SymairaTheme.textMuted
    }
}
