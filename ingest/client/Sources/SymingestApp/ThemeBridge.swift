import SwiftUI
// Re-exported so all views see SymairaTheme's tokens and Color(hex:)
// without per-file imports. Deliberate visual delta vs the old local copy:
// borderGlass 0.05 → 0.06 (unified brand value, like the skills client).
@_exported import SymairaTheme

typealias Theme = SymairaTheme

/// Ingest-specific full-window backdrop. The local BlueprintGrid (24px
/// cells, matching symaira.com) intentionally shadows SymairaTheme's
/// 30px variant.
public struct AmbientBackground: View {
    public init() {}
    public var body: some View {
        ZStack {
            Theme.bgDark
                .ignoresSafeArea()

            BlueprintGrid()
                .opacity(0.15)
                .ignoresSafeArea()

            ZStack {
                RadialGradient(
                    colors: [Theme.goldPrimary.opacity(0.12), .clear],
                    center: .topLeading,
                    startRadius: 0,
                    endRadius: 500
                )

                RadialGradient(
                    colors: [Theme.iceSecondary.opacity(0.08), .clear],
                    center: .bottomTrailing,
                    startRadius: 0,
                    endRadius: 600
                )
            }
            .ignoresSafeArea()
        }
    }
}

public struct BlueprintGrid: View {
    public init() {}
    public var body: some View {
        GeometryReader { geo in
            Path { path in
                let step: CGFloat = 24
                for x in stride(from: 0.0, to: geo.size.width, by: step) {
                    path.move(to: CGPoint(x: x, y: 0))
                    path.addLine(to: CGPoint(x: x, y: geo.size.height))
                }
                for y in stride(from: 0.0, to: geo.size.height, by: step) {
                    path.move(to: CGPoint(x: 0, y: y))
                    path.addLine(to: CGPoint(x: geo.size.width, y: y))
                }
            }
            .stroke(Color.white.opacity(0.022), lineWidth: 0.75)
        }
    }
}
