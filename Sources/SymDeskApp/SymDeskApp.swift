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
                    VaultConfig.reconcileFinderFavoritesOnLaunch()
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
            .onReceive(NotificationCenter.default.publisher(for: .vaultSwitched)) { _ in
                // A different vault became active: tear down the event watcher
                // and restart it against the new path so no events leak across
                // vaults (issue #296).
                watcher.reset()
                showDemoBanner = VaultConfig.isDemoMode
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
        .commands {
            CommandGroup(replacing: .appSettings) {
                Button("Rules & Settings\u{2026}") {
                    NotificationCenter.default.post(name: .openRulesSettings, object: nil)
                }
                .keyboardShortcut(",", modifiers: .command)
            }
            CommandMenu("File") {
                Button("New Note") {
                    NotificationCenter.default.post(name: .openNewNoteSheet, object: nil)
                }
                .keyboardShortcut("n", modifiers: .command)
                Button("New Daily Note") {
                    Task { _ = try? await core.noteDaily() }
                }
                .keyboardShortcut("d", modifiers: [.command, .shift])
                Divider()
                Button("Reveal Vault in Finder") {
                    if let path = core.vaultPath {
                        NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: path)
                    }
                }
                .disabled(core.vaultPath == nil)
                Divider()
                Button("Close") {
                    NSApplication.shared.keyWindow?.close()
                }
                .keyboardShortcut("w", modifiers: .command)
            }
            CommandMenu("View") {
                Button("Command Palette") {
                    NotificationCenter.default.post(name: .openCommandPalette, object: nil)
                }
                .keyboardShortcut("k", modifiers: .command)
                Divider()
                Button("Dashboard") {
                    NotificationCenter.default.post(name: .openDiscover, object: nil)
                }
            }
        }

        Settings {
            RulesSettingsView(vaultPath: core.vaultPath)
                .environmentObject(core)
                .environmentObject(watcher)
                .frame(minWidth: 520, minHeight: 400)
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
                .symairaText(.caption).bold()
                .foregroundColor(.black)
            Spacer()
            Button("Leave Demo Mode") {
                VaultConfig.reset()
                core.vaultPath = nil
                NotificationCenter.default.post(name: .vaultReset, object: nil)
            }
            .buttonStyle(.plain)
            .symairaText(.caption).fontWeight(.semibold)
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
