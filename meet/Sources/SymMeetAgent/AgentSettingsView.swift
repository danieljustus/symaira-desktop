import SwiftUI
import SymairaTheme

struct AgentSettingsView: View {
  @ObservedObject var updateChecker: AgentUpdateChecker

  var body: some View {
    VStack(alignment: .leading, spacing: SymairaSpacing.large) {
      Text("Settings")
        .symairaText(.heading, respectsForeground: false)
        .foregroundStyle(SymairaTheme.textPrimary)

      SymairaFormSection(
        "Updates",
        footer: "Automatic checks use the appkit cache and never force a network request on launch."
      ) {
        SymairaFormRow(
          "Check for updates on launch",
          description: "Look for a newer release when Symaira Meet starts."
        ) {
          Toggle(
            "",
            isOn: Binding(
              get: { updateChecker.autoCheckEnabled },
              set: { updateChecker.autoCheckEnabled = $0 }
            )
          )
          .labelsHidden()
          .toggleStyle(.switch)
        }

        SymairaFormDivider()

        SymairaFormRow(
          "Install updates automatically",
          description: "Download and verify an available app bundle without "
            + "relaunching automatically."
        ) {
          Toggle(
            "",
            isOn: Binding(
              get: { updateChecker.autoInstallEnabled },
              set: { updateChecker.autoInstallEnabled = $0 }
            )
          )
          .labelsHidden()
          .toggleStyle(.switch)
          .disabled(!updateChecker.autoCheckEnabled)
        }
      }
    }
    .padding(SymairaSpacing.xLarge)
    .frame(width: 560, height: 280, alignment: .topLeading)
    .background(SymairaTheme.bgDark)
  }
}
