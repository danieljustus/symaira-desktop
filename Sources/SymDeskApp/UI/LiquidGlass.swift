import SwiftUI
import SymairaTheme

/// A readable glass surface that uses Apple's Liquid Glass where available and
/// falls back to material on older supported macOS versions. The opaque tint is
/// deliberate: documents are work surfaces, not decorative windows.
struct SymDeskLiquidGlass: ViewModifier {
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency

    var cornerRadius: CGFloat = 16
    var prominence: Prominence = .standard

    enum Prominence {
        case standard
        case elevated
    }

    @ViewBuilder
    func body(content: Content) -> some View {
        let shape = RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
        let tint = prominence == .elevated
            ? SymairaTheme.bgCardHover.opacity(0.92)
            : SymairaTheme.bgCard.opacity(0.88)

        if reduceTransparency {
            content
                .background(tint.opacity(1), in: shape)
                .overlay {
                    shape.stroke(SymairaTheme.borderGlassHover.opacity(0.78), lineWidth: 1)
                        .allowsHitTesting(false)
                }
        } else if #available(macOS 26.0, *) {
            content
                .background(tint, in: shape)
                .glassEffect(.regular, in: .rect(cornerRadius: cornerRadius))
                .overlay {
                    shape.stroke(SymairaTheme.borderGlassHover.opacity(0.72), lineWidth: 1)
                        .allowsHitTesting(false)
                }
                .shadow(
                    color: prominence == .elevated ? .black.opacity(0.2) : .clear,
                    radius: prominence == .elevated ? 16 : 0,
                    y: prominence == .elevated ? 8 : 0
                )
        } else {
            content
                .background(.regularMaterial, in: shape)
                .background(tint, in: shape)
                .overlay {
                    shape.stroke(SymairaTheme.borderGlassHover.opacity(0.6), lineWidth: 1)
                        .allowsHitTesting(false)
                }
                .shadow(
                    color: prominence == .elevated ? .black.opacity(0.22) : .clear,
                    radius: prominence == .elevated ? 14 : 0,
                    y: prominence == .elevated ? 6 : 0
                )
        }
    }
}

extension View {
    func symDeskLiquidGlass(
        cornerRadius: CGFloat = 16,
        prominence: SymDeskLiquidGlass.Prominence = .standard
    ) -> some View {
        modifier(SymDeskLiquidGlass(cornerRadius: cornerRadius, prominence: prominence))
    }
}
