import SwiftUI
import SymMeetCore
import SymairaTheme
import SymairaUpdateCheck

class AppDelegate: NSObject, NSApplicationDelegate {
  var model: AgentModel?

  func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
    guard let model = model else { return .terminateNow }

    switch model.state {
    case .recording, .paused:
      return confirmTermination()
    default:
      return .terminateNow
    }
  }

  private func confirmTermination() -> NSApplication.TerminateReply {
    let alert = NSAlert()
    alert.messageText = "Quit Symaira Meet?"
    alert.informativeText = """
      A recording session is currently active. \
      Quitting will stop the session and preserve your recorded track files.
      """
    alert.addButton(withTitle: "Stop and Quit")
    alert.addButton(withTitle: "Cancel")

    let response = alert.runModal()
    if response == .alertFirstButtonReturn {
      Task { @MainActor in
        if let model = self.model {
          await model.stop()
        }
        NSApplication.shared.reply(toApplicationShouldTerminate: true)
      }
      return .terminateLater
    } else {
      return .terminateCancel
    }
  }

  // Keep running after last window closes (recording may continue)
  func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
    false
  }
}

@main
struct SymMeetAgentApp: App {
  @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate
  @StateObject private var model = AgentModel()
  @StateObject private var updateChecker = AgentUpdateChecker.shared

  init() {
    let model = AgentModel()
    _model = StateObject(wrappedValue: model)
    _appDelegate = NSApplicationDelegateAdaptor(AppDelegate.self)
  }

  var body: some Scene {
    // Main application window with Dock presence
    WindowGroup("Symaira Meet") {
      MainWindowView(model: model, updateChecker: updateChecker)
        .onAppear {
          appDelegate.model = model
          Task { await updateChecker.checkOnLaunchIfEnabled() }
        }
    }
    .windowResizability(.contentSize)
    .defaultSize(width: 720, height: 560)
    .handlesExternalEvents(matching: [])

    // Companion menu-bar extra for quick status and controls
    MenuBarExtra {
      RecordingMenu(model: model, updateChecker: updateChecker)
        .onAppear { appDelegate.model = model }
    } label: {
      HStack(spacing: 4) {
        Image(systemName: "waveform.circle.fill")
          .foregroundColor(isRecording ? .red : .primary)
        if case .recording(let elapsed) = model.state {
          Text(formatTime(elapsed))
            .symairaText(.mono)
        }
        if case .available = updateChecker.status {
          Image(systemName: "arrow.up.circle.fill")
            .foregroundColor(.orange)
            .symairaText(.caption, respectsForeground: false)
        }
      }
    }

    Settings {
      AgentSettingsView(updateChecker: updateChecker)
    }
  }

  private var isRecording: Bool {
    if case .recording = model.state { return true }
    return false
  }

  private func formatTime(_ time: TimeInterval) -> String {
    let minutes = (Int(time) % 3600) / 60
    let seconds = Int(time) % 60
    return String(format: "%02d:%02d", minutes, seconds)
  }
}
