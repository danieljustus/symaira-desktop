import SwiftUI
import SymairaTheme
import SymDeskCore

/// Preference keys owned by the app shell.
enum DeskPreferenceKey {
    /// Added with the CoreKit workspace; the app was dark-only before.
    static let appearance = "symdesk.appearance"
}

/// Content of the native Settings window (GUIDELINES SHELL-7). The tab is
/// persisted so in-app links ("Rules", "Search Index", "Companion Tools")
/// can open the window on the right page.
struct DeskSettingsView: View {
    @AppStorage("settings.selectedTab") private var selectedTab: DeskSettingsTab = .general

    var body: some View {
        TabView(selection: $selectedTab) {
            ForEach(DeskSettingsTab.allCases) { tab in
                page(for: tab)
                    .tabItem {
                        Label { Text(tab.title) } icon: { settingsTabIcon(tab.icon) }
                    }
                    .tag(tab)
            }
        }
        .frame(minWidth: 720, idealWidth: 820, minHeight: 560, idealHeight: 680)
    }

    @ViewBuilder
    private func page(for tab: DeskSettingsTab) -> some View {
        switch tab {
        case .general: GeneralSettingsPage()
        case .rules: RulesSettingsView()
        case .searchIndex: RetrievalStatusView()
        case .companionTools: CompanionToolsSettingsPage()
        }
    }
}

/// Template icon for the native Settings toolbar.
/// ponytail: `Image(symairaIcon:)` (CoreKit 0.23.0) hands NSToolbar a non-template
/// NSImage that stays black in dark appearance (symaira-corekit#491). Drop this
/// helper for `Image(symairaIcon:)` once the pin includes the fix.
@MainActor
private func settingsTabIcon(_ icon: SymairaIcon) -> Image {
    let renderer = ImageRenderer(content: SymairaIconView(icon, size: 16).foregroundStyle(SymairaTokens.ink))
    renderer.scale = 3
    guard let image = renderer.nsImage else { return Image(symairaIcon: icon, size: 16) }
    image.isTemplate = true
    return Image(nsImage: image)
}

/// Appearance and vault basics.
private struct GeneralSettingsPage: View {
    @EnvironmentObject var core: DeskCore
    @AppStorage(DeskPreferenceKey.appearance) private var appearance: SymairaAppearance = .system

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
                SymairaFormSection(
                    "Appearance",
                    footer: "Applies to every SymDesk window."
                ) {
                    SymairaFormRow("Appearance", description: "Follow the system or keep SymDesk light or dark.") {
                        SymairaAppearancePicker(selection: $appearance)
                    }
                }
                SymairaFormSection("Vault") {
                    SymairaFormRow("Location", description: core.vaultPath ?? "No vault configured") {
                        Button("Reveal in Finder") {
                            if let path = core.vaultPath {
                                NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: path)
                            }
                        }
                        .symairaButtonStyle(.secondary)
                        .disabled(core.vaultPath == nil)
                    }
                }
            }
            .padding(SymairaSpacing.xLarge)
            .frame(maxWidth: SymairaMetrics.readableContentWidth, alignment: .leading)
            .frame(maxWidth: .infinity)
        }
        .background(SymairaTokens.canvas)
    }
}

/// Companion tool status with its own doctor report, since the Settings
/// window has no workspace model.
private struct CompanionToolsSettingsPage: View {
    @EnvironmentObject var core: DeskCore
    @State private var report: DoctorReport?

    var body: some View {
        CompanionToolsView(
            doctorReport: report,
            onDoctorRefresh: { await refresh() }
        )
        .task { await refresh() }
    }

    private func refresh() async {
        report = try? await core.getDoctorReport()
    }
}
