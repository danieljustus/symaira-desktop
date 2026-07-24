import SwiftUI
import SymairaTheme
import SymDeskCore

struct SymDeskApp: App {
    @StateObject private var core = DeskCore.shared
    @StateObject private var watcher = EventWatcher.shared
    @StateObject private var notificationManager = NotificationManager.shared

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
            .environmentObject(notificationManager)
            .preferredColorScheme(.dark)
            .tint(SymairaTheme.goldPrimary)
            .background(SymairaTheme.bgDark)
            .task {
                await core.initialize()
                if VaultConfig.hasConfiguredVault {
                    core.loadVaultFromConfig()
                }
                if let tool = core.tool, vaultConfigured {
                    watcher.start(tool: tool, vaultPath: core.vaultPath)
                }
                notificationManager.requestPermission()
                await notificationManager.refreshNotifications(with: core)
            }
            .onReceive(NotificationCenter.default.publisher(for: .onboardingComplete)) { _ in
                vaultConfigured = true
                showDemoBanner = VaultConfig.isDemoMode
                core.loadVaultFromConfig()
                if let tool = core.tool {
                    watcher.start(tool: tool, vaultPath: core.vaultPath)
                }
                Task {
                    await notificationManager.refreshNotifications(with: core)
                }
            }
            .onReceive(NotificationCenter.default.publisher(for: .vaultReset)) { _ in
                vaultConfigured = false
                showDemoBanner = false
            }
        }
    }
}

private struct DemoBanner: View {
    @EnvironmentObject var core: DeskCore

    var body: some View {
        HStack {
            Image(systemName: "wand.and.stars")
                .foregroundColor(.black)
            Text("Demo Mode")
                .font(.caption.bold())
                .foregroundColor(.black)
            Spacer()
            Button("Leave Demo Mode") {
                VaultConfig.reset()
                core.vaultPath = nil
                NotificationCenter.default.post(name: .onboardingComplete, object: nil)
                NSApplication.shared.terminate(nil)
            }
            .buttonStyle(.plain)
            .font(.caption.weight(.semibold))
            .foregroundColor(.black.opacity(0.75))
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(
            LinearGradient(
                colors: [SymairaTheme.goldPrimary, SymairaTheme.goldSecondary],
                startPoint: .leading,
                endPoint: .trailing
            )
        )
    }
}
