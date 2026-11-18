import AppKit
import SwiftUI
import SymDictationCore
import SymairaTheme

/// The "Diktat" block of the agent settings: switch, onboarding status and options.
struct DictationSettingsSection: View {
  @ObservedObject var settings: DictationSettings
  @ObservedObject var service: DictationService
  @State private var confirmingEnable = false

  var body: some View {
    VStack(alignment: .leading, spacing: SymairaSpacing.large) {
      SymairaFormSection(
        "Diktat",
        footer: "Halten zum Sprechen, zweimal tippen für freihändig, Esc bricht ab. "
          + "Aufnahme läuft nur, solange die Taste gehalten wird oder freihändig eingeschaltet ist. "
          + "Es gibt keinen Verlauf und keine Audiodatei auf der Festplatte."
      ) {
        SymairaFormRow(
          "Diktat aktivieren",
          description: "Gesprochenen Text per Tastendruck in das aktive Fenster einfügen."
        ) {
          Toggle(
            "",
            isOn: Binding(
              get: { settings.isEnabled },
              set: { newValue in
                if newValue {
                  confirmingEnable = true
                } else {
                  service.setEnabled(false)
                }
              }
            )
          )
          .labelsHidden()
          .toggleStyle(.switch)
        }

        if settings.isEnabled {
          SymairaFormDivider()
          statusRows
          SymairaFormDivider()
          optionRows
        }
      }
    }
    .sheet(isPresented: $confirmingEnable) {
      DictationEnableSheet(
        onCancel: { confirmingEnable = false },
        onConfirm: {
          confirmingEnable = false
          service.setEnabled(true)
        })
    }
    .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification))
    {
      _ in service.recheck()
    }
  }

  // MARK: - Status and onboarding (same row pattern as PermissionView)

  @ViewBuilder
  private var statusRows: some View {
    SymairaFormRow("Status") { statusLabel }

    switch service.status {
    case .needsMicrophone:
      SymairaFormDivider()
      SymairaFormRow(
        "Mikrofon",
        description: "Ohne Mikrofonzugriff kann nichts aufgenommen werden."
      ) {
        Button("Öffnen") { service.openPrivacySettings(pane: "Privacy_Microphone") }
          .symairaButtonStyle(.primary)
      }
    case .needsAccessibility:
      SymairaFormDivider()
      SymairaFormRow(
        "Bedienungshilfen",
        description: "Nötig, um die Taste zu erkennen und Text einzufügen. "
          + "Bis zur Freigabe bleibt gesprochener Text nicht verfügbar."
      ) {
        VStack(alignment: .trailing, spacing: SymairaSpacing.small) {
          Button("Freigeben") { service.requestAccessibility() }
            .symairaButtonStyle(.primary)
          Button("Systemeinstellungen") {
            service.openPrivacySettings(pane: "Privacy_Accessibility")
          }
          .buttonStyle(.plain)
          .underline()
        }
      }
    case .preparingModel(let progress):
      SymairaFormDivider()
      SymairaFormRow(
        "Modell", description: "Wird beim ersten Aktivieren geladen. Das kann einen Moment dauern."
      ) {
        ProgressView(value: progress).frame(width: 140)
      }
    default:
      EmptyView()
    }
  }

  @ViewBuilder
  private var statusLabel: some View {
    switch service.status {
    case .off: SymairaStatusLabel("Aus", tone: .neutral)
    case .needsMicrophone: SymairaStatusLabel("Mikrofon fehlt", tone: .warning)
    case .preparingModel: SymairaStatusLabel("Modell wird geladen", tone: .informative)
    case .needsAccessibility: SymairaStatusLabel("Bedienungshilfen fehlen", tone: .warning)
    case .ready: SymairaStatusLabel("Bereit", tone: .positive)
    case .failed(let message): SymairaStatusLabel(message, tone: .critical)
    }
  }

  @ViewBuilder
  private var optionRows: some View {
    SymairaFormRow(
      "Taste",
      description: "Die Globe-Taste sollte in den Tastatureinstellungen auf „Nichts tun“ stehen."
    ) {
      Picker("", selection: $settings.hotkey) {
        ForEach(DictationHotkey.allCases, id: \.self) { Text($0.title).tag($0) }
      }
      .labelsHidden()
      .frame(width: 180)
    }

    SymairaFormDivider()

    SymairaFormRow("Spracherkennung", description: "Parakeet läuft lokal auf der Neural Engine.") {
      Picker("", selection: $settings.engine) {
        ForEach(DictationEngineChoice.allCases) { Text($0.title).tag($0) }
      }
      .labelsHidden()
      .frame(width: 220)
    }

    SymairaFormDivider()

    SymairaFormRow(
      "Immer tippen statt einfügen",
      description: "In Chrome, Brave, Edge, Arc und Electron-Apps wird ohnehin getippt."
    ) {
      Toggle("", isOn: $settings.alwaysType).labelsHidden().toggleStyle(.switch)
    }

    SymairaFormDivider()

    SymairaFormRow(
      "Vault für Wörterbuch",
      description: "Optional. Liest .symdesk/dictation/dictionary.md und corrections.md. "
        + "Leer lässt SYMDESK_VAULT gelten."
    ) {
      TextField("/Pfad/zum/Vault", text: $settings.vaultPath)
        .textFieldStyle(.symaira)
        .frame(width: 220)
    }
  }
}

/// Confirmation before dictation is switched on, in the style of `ConsentSheet`.
struct DictationEnableSheet: View {
  let onCancel: () -> Void
  let onConfirm: () -> Void

  var body: some View {
    VStack(spacing: 20) {
      Text("Diktat aktivieren")
        .symairaText(.heading)

      Text(
        "Symaira Meet braucht dafür Mikrofon und Bedienungshilfen. "
          + "Aufgenommen wird nur, solange du die Taste hältst oder freihändig eingeschaltet hast. "
          + "Die Spracherkennung läuft lokal. Beim ersten Mal wird das Sprachmodell heruntergeladen."
      )
      .symairaText(.body, respectsForeground: false)
      .multilineTextAlignment(.center)
      .foregroundColor(.secondary)

      HStack(spacing: 16) {
        Button("Abbrechen", action: onCancel)
          .buttonStyle(.bordered)
        Button("Aktivieren", action: onConfirm)
          .buttonStyle(.borderedProminent)
      }
    }
    .padding()
    .frame(width: 360)
  }
}
