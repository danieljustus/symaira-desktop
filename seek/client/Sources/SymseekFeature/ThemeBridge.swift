import SwiftUI
// Re-exported so all views see the shared Color.symaira* tokens and
// Color(hex:) without per-file imports (zero-diff migration).
@_exported import SymairaTheme

// Seek-specific tokens that are not part of the shared brand set
// (kept local for pixel-identical rendering; revisit in the hub).
extension Color {
    static let symairaBorder = Color.white.opacity(0.05)
    static let symairaBorderHover = SymairaTheme.goldPrimary.opacity(0.18)
    static let symairaGlow = SymairaTheme.goldPrimary.opacity(0.05)
}

/// Seek-specific hoverable card (padding + easeInOut hover differ from the
/// shared GlassCardModifier).
struct SymairaCardModifier: ViewModifier {
    @State private var isHovered = false

    func body(content: Content) -> some View {
        content
            .padding()
            .background(isHovered ? Color.symairaCardHover : Color.symairaCard)
            .cornerRadius(12)
            .overlay(
                RoundedRectangle(cornerRadius: 12)
                    .stroke(isHovered ? Color.symairaBorderHover : Color.symairaBorder, lineWidth: 1)
            )
            .animation(.easeInOut(duration: 0.2), value: isHovered)
            .onHover { hover in
                isHovered = hover
            }
    }
}

extension View {
    func symairaCard() -> some View {
        self.modifier(SymairaCardModifier())
    }
}
