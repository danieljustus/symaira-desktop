import SwiftUI

@main
struct SymDeskMobileApp: App {
    @StateObject private var vault = MobileVaultStore()

    var body: some Scene {
        WindowGroup {
            MobileRootView()
                .environmentObject(vault)
                .preferredColorScheme(.dark)
                .tint(MobileTheme.gold)
        }
    }
}
