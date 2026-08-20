import SwiftUI
import SymairaTheme

struct PermissionView: View {
  @ObservedObject var model: AgentModel

  var body: some View {
    VStack(spacing: 20) {
      Text("Permissions Required")
        .symairaText(.heading, respectsForeground: false)
        .foregroundColor(.primary)

      Text("Symaira Meet needs microphone and screen recording permissions to capture synchronized system and microphone audio.")
        .symairaText(.body, respectsForeground: false)
        .multilineTextAlignment(.center)
        .foregroundColor(.secondary)
        .padding(.horizontal)

      SymairaFormSection("Permissions") {
        permissionRow(
          name: "Microphone",
          authorized: model.microphoneAuthorized,
          action: {
            Task {
              await model.requestMicrophonePermission()
            }
          }
        )

        SymairaFormDivider()

        permissionRow(
          name: "Screen Recording",
          authorized: model.screenRecordingAuthorized,
          action: {
            Task {
              await model.requestScreenRecordingPermission()
            }
          }
        )
      }

      if !model.microphoneAuthorized || !model.screenRecordingAuthorized {
        Button(action: {
          if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy") {
            NSWorkspace.shared.open(url)
          }
        }) {
          Text("Open System Settings")
            .symairaText(.callout, respectsForeground: false)
            .underline()
            .foregroundColor(.accentColor)
        }
        .buttonStyle(.plain)
      }
    }
    .padding()
    .frame(width: 320)
  }

  private func permissionRow(name: String, authorized: Bool, action: @escaping () -> Void) -> some View {
    SymairaFormRow(name) {
      if authorized {
        SymairaStatusLabel("Granted", tone: .positive)
      } else {
        Button("Grant", action: action)
          .symairaButtonStyle(.primary)
      }
    }
  }
}
