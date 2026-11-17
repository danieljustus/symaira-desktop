import SwiftUI
import SymairaTheme

/// Flat `canvas` page behind a screen. All colors, type and surfaces come
/// from SymairaTheme; this file keeps no tokens of its own. No gradients, glows
/// or shadows (MOTION-4/6): cards use `.symairaCard()`.
struct MobileBackdrop<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        ZStack {
            SymairaTokens.canvas.ignoresSafeArea()
            content
        }
    }
}

/// Semantic role color for a document status. The status text always sits next
/// to the color, so the state is never conveyed by color alone (NN-6).
func mobileStatusColor(_ status: String) -> Color {
    switch status.lowercased() {
    case "paid", "done": return SymairaTokens.positive
    case "needs_review": return SymairaTokens.warning
    case "submitted", "waiting_for_reply": return SymairaTokens.ink
    default: return SymairaTokens.ink2
    }
}
