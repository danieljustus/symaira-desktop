import SwiftUI

struct ContentView: View {
    @Environment(ConfigStore.self) private var configStore
    @Environment(EngineManager.self) private var engineManager
    
    @State private var selection: String? = "dashboard"
    
    var body: some View {
        NavigationSplitView {
            List(selection: $selection) {
                NavigationLink(value: "dashboard") {
                    Label("Dashboard", systemImage: "rectangle.grid.2x2.fill")
                }
                NavigationLink(value: "jobs") {
                    Label("Jobs Queue", systemImage: "clock.arrow.circlepath")
                }
                NavigationLink(value: "rules") {
                    Label("Classification Rules", systemImage: "tag.fill")
                }
                NavigationLink(value: "import") {
                    Label("Paperless Import", systemImage: "arrow.down.doc.fill")
                }
                NavigationLink(value: "review") {
                    Label("Migration Review", systemImage: "doc.text.magnifyingglass")
                }
                NavigationLink(value: "preview") {
                    Label("Preview", systemImage: "doc.richtext")
                }
                NavigationLink(value: "settings") {
                    Label("Settings & Doctor", systemImage: "gearshape.fill")
                }
            }
            .listStyle(.sidebar)
            .navigationTitle("Symingest")
        } detail: {
            ZStack {
                AmbientBackground()
                
                Group {
                    switch selection {
                    case "dashboard":
                        DashboardView()
                    case "jobs":
                        JobsView()
                    case "rules":
                        RulesView()
                    case "import":
                        ImportView()
                    case "review":
                        ReviewView()
                    case "preview":
                        PreviewView()
                    case "settings":
                        SettingsView()
                    default:
                        DashboardView()
                    }
                }
                .padding()
            }
        }
        .frame(minWidth: 950, minHeight: 650)
    }
}
