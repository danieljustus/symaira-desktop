import SwiftUI
import AppKit
import SymseekFeature

/// App delegate that ensures the window registers with the Core Graphics
/// WindowServer on launch — without this the SwiftUI WindowGroup does not
/// receive mouse/keyboard events and the AX tree remains empty.
class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        // Force CGS connection and register the app's windows with the
        // WindowServer so events are delivered to the SwiftUI scene.
        NSApp.activate(ignoringOtherApps: true)
    }
}

@main
struct SymseekApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    var body: some Scene {
        WindowGroup {
            SymseekModuleView()
        }
        .windowStyle(.hiddenTitleBar)
        .windowResizability(.contentSize)
    }
}
