import SwiftUI

@main
struct SymprintApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    var body: some Scene {
        WindowGroup {
            ContentView()
                .frame(minWidth: 1000, minHeight: 680)
                .onOpenURL { url in
                    AppState.shared.handleOpen(url)
                }
        }
        .windowStyle(.hiddenTitleBar)
    }
}

/// Bridges LaunchServices document-open events (Finder "Open With",
/// double-click, drag onto the Dock icon, `open -a`) into the shared app
/// state. Handles both cold launches (files delivered at startup) and warm
/// launches (app already running); SwiftUI's `onOpenURL` covers the same
/// ground and both paths converge on `AppState`.
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    func application(_ application: NSApplication, open urls: [URL]) {
        for url in urls {
            AppState.shared.handleOpen(url)
        }
    }
}
