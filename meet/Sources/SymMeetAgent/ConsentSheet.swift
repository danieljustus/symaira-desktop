import SwiftUI
import SymairaTheme

struct ConsentSheet: View {
  @ObservedObject var model: AgentModel
  @State private var purposeInput: String = ""

  init(model: AgentModel) {
    self.model = model
  }

  var body: some View {
    VStack(spacing: 20) {
      Text("Start Recording")
        .symairaText(.heading)

      Text("Please specify the purpose of the meeting for interactive authorization record. Processing remains local.")
        .symairaText(.body, respectsForeground: false)
        .multilineTextAlignment(.center)
        .foregroundColor(.secondary)

      SymairaFormSection("Meeting") {
        SymairaFormRow("Purpose") {
          TextField("e.g. Daily Standup", text: $purposeInput)
            .textFieldStyle(.symaira)
        }
      }

      HStack(spacing: 16) {
        Button("Cancel") {
          model.cancelConsent()
        }
        .buttonStyle(.bordered)

        Button("Start") {
          let trimmed = purposeInput.trimmingCharacters(in: .whitespacesAndNewlines)
          let purpose = trimmed.isEmpty ? "General Meeting" : trimmed
          Task {
            await model.initiateRecording(purpose: purpose)
            await model.confirmConsent(attested: true)
          }
        }
        .buttonStyle(.borderedProminent)
        .disabled(purposeInput.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
      }
    }
    .padding()
    .frame(width: 320)
  }
}
