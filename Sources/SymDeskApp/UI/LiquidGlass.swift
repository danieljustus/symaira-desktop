import SwiftUI
import SymairaTheme

/// Former app-local glass panel. Documents are reading surfaces, so the
/// standard prominence is an opaque `surface` card with a `line` outline
/// (GUIDELINES NN-7, MOTION-3); `elevated` is for floating panels (command
/// palette, popover-like overlays) and is the only variant with a shadow
/// (MOTION-4). Kept as a thin wrapper so existing call sites need no change.
struct SymDeskLiquidGlass: ViewModifier {
    @Environment(\.colorSchemeContrast) private var contrast

    var cornerRadius: CGFloat = SymairaRadius.card
    var prominence: Prominence = .standard

    enum Prominence {
        case standard
        case elevated
    }

    func body(content: Content) -> some View {
        let shape = RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
        let isElevated = prominence == .elevated
        content
            .background(isElevated ? SymairaTokens.raised : SymairaTokens.surface, in: shape)
            .overlay {
                shape.strokeBorder(SymairaTokens.line, lineWidth: contrast == .increased ? 1.5 : 1)
                    .allowsHitTesting(false)
            }
            .shadow(
                color: isElevated ? Color.black.opacity(0.18) : .clear,
                radius: isElevated ? 16 : 0,
                y: isElevated ? 8 : 0
            )
    }
}

extension View {
    func symDeskLiquidGlass(
        cornerRadius: CGFloat = SymairaRadius.card,
        prominence: SymDeskLiquidGlass.Prominence = .standard
    ) -> some View {
        modifier(SymDeskLiquidGlass(cornerRadius: cornerRadius, prominence: prominence))
    }
}
