import SwiftUI

@main
struct SymingestApp: App {
    @State private var configStore = ConfigStore()
    @State private var engineManager = EngineManager()
    
    var body: some Scene {
        Window("Symingest", id: "main") {
            ContentView()
                .environment(configStore)
                .environment(engineManager)
                .preferredColorScheme(.dark)
        }
        .windowStyle(.hiddenTitleBar)
        .windowToolbarStyle(.unifiedCompact)
    }
}
