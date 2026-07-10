import SwiftUI
import SymairaTheme
import SymDeskCore

struct SymDeskApp: App {
    @StateObject private var core = DeskCore.shared
    @StateObject private var watcher = EventWatcher.shared

    @State private var vaultConfigured = VaultConfig.hasConfiguredVault
    @State private var showDemoBanner = VaultConfig.isDemoMode

    var body: some Scene {
        WindowGroup {
            Group {
                if vaultConfigured {
                    ContentView()
                        .overlay(alignment: .top) {
                            if showDemoBanner {
                                DemoBanner()
                            }
                        }
                } else {
                    OnboardingView()
                }
            }
            .environmentObject(core)
            .environmentObject(watcher)
            .task {
                await core.initialize()
                if VaultConfig.hasConfiguredVault {
                    core.loadVaultFromConfig()
                }
                if let tool = core.tool, vaultConfigured {
                    watcher.start(tool: tool, vaultPath: core.vaultPath)
                }
            }
            .onReceive(NotificationCenter.default.publisher(for: .onboardingComplete)) { _ in
                vaultConfigured = true
                showDemoBanner = VaultConfig.isDemoMode
                core.loadVaultFromConfig()
                if let tool = core.tool {
                    watcher.start(tool: tool, vaultPath: core.vaultPath)
                }
            }
        }
    }
}

private struct DemoBanner: View {
    @EnvironmentObject var core: DeskCore

    var body: some View {
        HStack {
            Image(systemName: "wand.and.stars")
                .foregroundColor(.white)
            Text("Demo Mode")
                .font(.caption.bold())
                .foregroundColor(.white)
            Spacer()
            Button("Leave Demo Mode") {
                VaultConfig.reset()
                core.vaultPath = nil
                NotificationCenter.default.post(name: .onboardingComplete, object: nil)
                NSApplication.shared.terminate(nil)
            }
            .font(.caption)
            .foregroundColor(.white.opacity(0.9))
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(Color.orange)
    }
}
