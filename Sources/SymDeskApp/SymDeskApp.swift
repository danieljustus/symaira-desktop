import SwiftUI
import SymairaTheme
import SymDeskCore

@main
struct SymDeskApp: App {
    @StateObject private var core = DeskCore.shared
    @StateObject private var watcher = EventWatcher.shared
    
    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(core)
                .environmentObject(watcher)
                .task {
                    await core.initialize()
                    if let tool = core.tool {
                        watcher.start(tool: tool)
                    }
                }
        }
    }
}
