import SwiftUI

enum MobileTheme {
    static let background = Color(red: 13 / 255, green: 12 / 255, blue: 10 / 255)
    static let backgroundRaised = Color(red: 27 / 255, green: 25 / 255, blue: 21 / 255)
    static let card = Color(red: 35 / 255, green: 32 / 255, blue: 27 / 255)
    static let cardHover = Color(red: 47 / 255, green: 43 / 255, blue: 36 / 255)
    static let gold = Color(red: 229 / 255, green: 195 / 255, blue: 151 / 255)
    static let goldSoft = Color(red: 248 / 255, green: 230 / 255, blue: 205 / 255)
    static let ice = Color(red: 180 / 255, green: 218 / 255, blue: 226 / 255)
    static let textPrimary = Color(red: 245 / 255, green: 244 / 255, blue: 240 / 255)
    static let textSecondary = Color(red: 188 / 255, green: 184 / 255, blue: 174 / 255)
    static let textMuted = Color(red: 137 / 255, green: 133 / 255, blue: 123 / 255)
    static let border = Color.white.opacity(0.13)
}

struct MobileBackdrop<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        ZStack {
            LinearGradient(
                colors: [MobileTheme.backgroundRaised, MobileTheme.background],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            )
            .ignoresSafeArea()

            RadialGradient(
                colors: [MobileTheme.gold.opacity(0.13), .clear],
                center: .topTrailing,
                startRadius: 0,
                endRadius: 310
            )
            .ignoresSafeArea()

            content
        }
    }
}

struct MobileLiquidGlass: ViewModifier {
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency

    var cornerRadius: CGFloat = 22
    var elevated = false

    @ViewBuilder
    func body(content: Content) -> some View {
        let shape = RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
        let tint = elevated ? MobileTheme.cardHover.opacity(0.88) : MobileTheme.card.opacity(0.8)

        if reduceTransparency {
            content
                .background(MobileTheme.card, in: shape)
                .overlay(shape.stroke(MobileTheme.border, lineWidth: 1))
        } else if #available(iOS 26.0, *) {
            content
                .background(tint, in: shape)
                .glassEffect(.regular, in: .rect(cornerRadius: cornerRadius))
                .overlay(shape.stroke(MobileTheme.border, lineWidth: 1))
                .shadow(color: elevated ? .black.opacity(0.24) : .clear, radius: 18, y: 10)
        } else {
            content
                .background(.regularMaterial, in: shape)
                .background(tint, in: shape)
                .overlay(shape.stroke(MobileTheme.border, lineWidth: 1))
                .shadow(color: elevated ? .black.opacity(0.22) : .clear, radius: 16, y: 8)
        }
    }
}

extension View {
    func mobileLiquidGlass(cornerRadius: CGFloat = 22, elevated: Bool = false) -> some View {
        modifier(MobileLiquidGlass(cornerRadius: cornerRadius, elevated: elevated))
    }
}

func mobileStatusColor(_ status: String) -> Color {
    switch status.lowercased() {
    case "paid", "done": return .green
    case "submitted": return MobileTheme.ice
    case "needs_review": return .orange
    case "waiting_for_reply": return MobileTheme.goldSoft
    default: return MobileTheme.gold
    }
}
