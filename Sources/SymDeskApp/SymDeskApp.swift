import SwiftUI
import SymairaTheme
import SymDeskCore

struct SymDeskApp: App {
    @StateObject private var core = DeskCore.shared
    @StateObject private var watcher = EventWatcher.shared
    @StateObject private var notificationManager = NotificationManager.shared

    /// Set when a vault is configured but its folder is gone, so onboarding can
    /// name the path instead of opening it as an empty vault (issue #444).
    @State private var missingVaultPath = VaultConfig.missingLocalVaultPath
    @State private var vaultConfigured = VaultConfig.hasConfiguredVault
        && VaultConfig.missingLocalVaultPath == nil
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
                    OnboardingView(missingVaultPath: missingVaultPath)
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
                // Skipped when the configured folder is gone: loading it would
                // point every command at a path that no longer exists (#444).
                if vaultConfigured {
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
                missingVaultPath = nil
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
                missingVaultPath = nil
                vaultConfigured = false
                showDemoBanner = false
            }
        }
        .commands {
            // No custom app-settings entry: the `Settings` scene below already
            // contributes the standard "Settings…" item on Cmd+,. Declaring
            // both put two settings entries in the app menu (issue #446). The
            // in-app Rules screen stays reachable from the sidebar, and
            // `openRulesSettings` is still posted from the AI chat panel.
            //
            // These belong in the standard File and View menus. `CommandMenu`
            // always creates a *new* top-level menu, which left the menu bar
            // with two File menus and two View menus, and Cmd+N / Cmd+W each
            // bound twice (issue #442). `CommandGroup` merges instead.
            //
            // New Note replaces New Window rather than sitting beside it: this
            // is a single-vault workspace, so Cmd+N belongs to the note, and
            // replacing the slot removes the duplicate binding outright.
            CommandGroup(replacing: .newItem) {
                Button("New Note") {
                    NotificationCenter.default.post(name: .openNewNoteSheet, object: nil)
                }
                .keyboardShortcut("n", modifiers: .command)
                Button("New Daily Note") {
                    Task { _ = try? await core.noteDaily() }
                }
                .keyboardShortcut("d", modifiers: [.command, .shift])
            }
            CommandGroup(after: .newItem) {
                Divider()
                Button("Reveal Vault in Finder") {
                    if let path = core.vaultPath {
                        NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: path)
                    }
                }
                .disabled(core.vaultPath == nil)
            }
            // No custom Close: the standard File menu already provides one on
            // Cmd+W, and duplicating it was the second half of #442.
            CommandGroup(after: .sidebar) {
                Button("Command Palette") {
                    NotificationCenter.default.post(name: .openCommandPalette, object: nil)
                }
                .keyboardShortcut("k", modifiers: .command)
                Divider()
                Button("Dashboard") {
                    NotificationCenter.default.post(name: .openDashboard, object: nil)
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
