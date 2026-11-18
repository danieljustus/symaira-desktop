import AppKit
import SwiftUI
import SymDictationCore
import SymairaTheme

/// A floating panel that never takes focus from the app the user is dictating into.
private final class NonActivatingPanel: NSPanel {
  override var canBecomeKey: Bool { false }
  override var canBecomeMain: Bool { false }
}

private func makePanel(interactive: Bool) -> NSPanel {
  let panel = NonActivatingPanel(
    contentRect: .zero,
    styleMask: [.borderless, .nonactivatingPanel],
    backing: .buffered,
    defer: true)
  panel.isFloatingPanel = true
  panel.level = .statusBar
  panel.hidesOnDeactivate = false
  panel.isOpaque = false
  panel.backgroundColor = .clear
  panel.hasShadow = true
  panel.ignoresMouseEvents = !interactive
  panel.becomesKeyOnlyIfNeeded = true
  panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .stationary, .ignoresCycle]
  return panel
}

// MARK: - Indicator pill

@MainActor
final class DictationIndicatorModel: ObservableObject {
  enum Kind: Equatable {
    case recording(DictationRecordingMode)
    case processing
    case notice(String)
  }
  @Published var kind: Kind = .processing
}

private struct IndicatorPill: View {
  @ObservedObject var model: DictationIndicatorModel

  var body: some View {
    HStack(spacing: 8) {
      switch model.kind {
      case .recording(let mode):
        Circle().fill(Color.red).frame(width: 10, height: 10)
        Text(mode == .handsFree ? "Freihändig, Taste tippen zum Beenden" : "Ich höre zu")
      case .processing:
        ProgressView().controlSize(.small)
        Text("Wird umgewandelt")
      case .notice(let message):
        Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
        Text(message).lineLimit(3)
      }
    }
    .symairaText(.callout, respectsForeground: false)
    .foregroundStyle(.primary)
    .padding(.horizontal, 14)
    .padding(.vertical, 8)
    .background(.regularMaterial, in: Capsule())
    .overlay(Capsule().strokeBorder(Color.primary.opacity(0.12)))
    .fixedSize()
    .padding(6)
  }
}

// MARK: - Card

private struct DictationCardView: View {
  let text: String
  let reason: CardReason
  let onCopy: () -> Void
  let onClose: () -> Void
  @State private var copied = false

  var body: some View {
    VStack(alignment: .leading, spacing: 10) {
      Text(DictationHints.cardTitle(for: reason))
        .symairaText(.callout, respectsForeground: false)
        .foregroundStyle(.secondary)
      ScrollView {
        Text(text)
          .symairaText(.body, respectsForeground: false)
          .textSelection(.enabled)
          .frame(maxWidth: .infinity, alignment: .leading)
      }
      .frame(maxHeight: 140)
      HStack {
        Button(copied ? "Kopiert" : "Kopieren") {
          onCopy()
          copied = true
        }
        .symairaButtonStyle(.primary)
        Button("Schließen", action: onClose)
          .symairaButtonStyle(.secondary)
      }
    }
    .padding(14)
    .frame(width: 380)
    .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
    .overlay(RoundedRectangle(cornerRadius: 14).strokeBorder(Color.primary.opacity(0.12)))
    .padding(6)
  }
}

/// User-facing German texts for situations where text is held back.
enum DictationHints {
  static func cardTitle(for reason: CardReason) -> String {
    switch reason {
    case .secureInput:
      return
        "Sichere Eingabe ist aktiv (zum Beispiel ein Passwortfeld). Der Text wurde weder eingefügt noch in die Zwischenablage gelegt."
    case .accessibilityMissing:
      return
        "Für das Einfügen fehlt die Freigabe unter Bedienungshilfen. Der Text bleibt hier verfügbar."
    case .noTextField:
      return "Hier ist kein Textfeld aktiv. Der Text bleibt hier verfügbar."
    case .documentChanged:
      return
        "Das Fenster hat gewechselt, deshalb wurde nichts eingefügt. Der Text bleibt hier verfügbar."
    case .insertFailed:
      return "Das Einfügen hat nicht geklappt. Der Text bleibt hier verfügbar."
    }
  }

  static func message(for notice: DictationNotice) -> String {
    switch notice {
    case .microphoneUnavailable(let reason): return reason
    case .transcriptionFailed: return "Die Spracherkennung ist fehlgeschlagen."
    case .nothingHeard: return "Es wurde nichts verstanden."
    }
  }
}

// MARK: - Presenter

@MainActor
final class DictationPanelPresenter: DictationPresenter {
  private let indicatorModel = DictationIndicatorModel()
  private var indicatorPanel: NSPanel?
  private var cardPanel: NSPanel?
  private var noticeTask: Task<Void, Never>?

  func showRecording(_ mode: DictationRecordingMode) {
    noticeTask?.cancel()
    noticeTask = nil
    indicatorModel.kind = .recording(mode)
    present(indicator: true)
  }

  func showProcessing() {
    noticeTask?.cancel()
    noticeTask = nil
    indicatorModel.kind = .processing
    present(indicator: true)
  }

  func hideIndicator() {
    guard noticeTask == nil else { return }
    indicatorPanel?.orderOut(nil)
  }

  func showNotice(_ notice: DictationNotice) {
    noticeTask?.cancel()
    indicatorModel.kind = .notice(DictationHints.message(for: notice))
    present(indicator: true)
    noticeTask = Task { [weak self] in
      try? await Task.sleep(nanoseconds: 4_000_000_000)
      guard !Task.isCancelled, let self else { return }
      self.noticeTask = nil
      self.indicatorPanel?.orderOut(nil)
    }
  }

  func showCard(text: String, reason: CardReason) {
    closeCard()
    let panel = makePanel(interactive: true)
    let view = DictationCardView(
      text: text, reason: reason,
      onCopy: {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
      },
      onClose: { [weak self] in self?.closeCard() })
    let host = NSHostingView(rootView: view)
    panel.contentView = host
    panel.setContentSize(host.fittingSize)
    position(panel, bottomOffset: 80)
    panel.orderFrontRegardless()
    cardPanel = panel
  }

  func closeCard() {
    cardPanel?.orderOut(nil)
    cardPanel = nil
  }

  func tearDown() {
    noticeTask?.cancel()
    noticeTask = nil
    indicatorPanel?.orderOut(nil)
    closeCard()
  }

  private func present(indicator: Bool) {
    let panel: NSPanel
    if let existing = indicatorPanel {
      panel = existing
    } else {
      panel = makePanel(interactive: false)
      panel.contentView = NSHostingView(rootView: IndicatorPill(model: indicatorModel))
      indicatorPanel = panel
    }
    if let host = panel.contentView as? NSHostingView<IndicatorPill> {
      panel.setContentSize(host.fittingSize)
    }
    position(panel, bottomOffset: 28)
    panel.orderFrontRegardless()
  }

  /// Bottom center of the screen that holds the focused window.
  private func position(_ panel: NSPanel, bottomOffset: CGFloat) {
    let screen = NSScreen.main ?? NSScreen.screens.first
    guard let frame = screen?.visibleFrame else { return }
    let size = panel.frame.size
    panel.setFrameOrigin(
      NSPoint(x: frame.midX - size.width / 2, y: frame.minY + bottomOffset))
  }
}
