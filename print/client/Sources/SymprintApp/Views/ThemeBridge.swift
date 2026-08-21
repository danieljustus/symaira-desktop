import SwiftUI
// Re-exported so all views see SymairaTheme's tokens, glassCard and
// GlassCardModifier without per-file imports (zero-diff migration).
@_exported import SymairaTheme

typealias Theme = SymairaTheme

// Legacy names used by this app before the shared package.
extension SymairaTheme {
    static let glowCyan = SymairaTheme.glowSoft
    static let glowCyanIntense = SymairaTheme.glowIntense
}

/// Print-specific full-window backdrop (24px grid matching symaira.com).
struct BlueprintBackground: View {
    var body: some View {
        ZStack {
            Theme.bgDark.ignoresSafeArea()

            GeometryReader { geo in
                Path { path in
                    let step: CGFloat = 24

                    var x: CGFloat = 0
                    while x < geo.size.width {
                        path.move(to: CGPoint(x: x, y: 0))
                        path.addLine(to: CGPoint(x: x, y: geo.size.height))
                        x += step
                    }

                    var y: CGFloat = 0
                    while y < geo.size.height {
                        path.move(to: CGPoint(x: 0, y: y))
                        path.addLine(to: CGPoint(x: geo.size.width, y: y))
                        y += step
                    }
                }
                .stroke(Color.white.opacity(0.022), lineWidth: 1)
            }
            .ignoresSafeArea()

            VStack {
                HStack {
                    Circle()
                        .fill(Theme.glowCyanIntense)
                        .frame(width: 350, height: 350)
                        .blur(radius: 90)
                        .offset(x: -100, y: -100)
                    Spacer()
                }
                Spacer()
                HStack {
                    Spacer()
                    Circle()
                        .fill(Theme.glowCyan)
                        .frame(width: 450, height: 450)
                        .blur(radius: 120)
                        .offset(x: 150, y: 150)
                }
            }
            .ignoresSafeArea()
        }
    }
}
