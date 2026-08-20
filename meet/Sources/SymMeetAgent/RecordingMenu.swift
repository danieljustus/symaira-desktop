import SwiftUI
import SymMeetCore
import SymairaTheme
import SymairaUpdateCheck

struct RecordingMenu: View {
  @ObservedObject var model: AgentModel
  @ObservedObject var updateChecker: AgentUpdateChecker

  var body: some View {
    VStack(spacing: 16) {
      // Update notification banner (discreet, non-modal)
      updateSection

      switch model.state {
      case .idle:
        idleSection

      case .permissionRequired:
        PermissionView(model: model)

      case .consentConfirmation:
        VStack(spacing: 12) {
          Text("Confirm Consent")
            .symairaText(.subheading)
          Text(
            "By proceeding, you authorize this recording. "
              + "It will process audio locally on your device."
          )
          .symairaText(.secondary, respectsForeground: false)
          .multilineTextAlignment(.center)
          .foregroundColor(.secondary)

          HStack {
            Button("Cancel") {
              model.cancelConsent()
            }
            Spacer()
            Button("Confirm") {
              Task {
                await model.confirmConsent(attested: true)
              }
            }
            .buttonStyle(.borderedProminent)
          }
        }

      case .starting:
        HStack {
          ProgressView()
            .scaleEffect(0.8)
          Text("Starting capture session...")
            .symairaText(.callout)
        }

      case .recording(let elapsed):
        recordingSection(elapsed: elapsed)

      case .paused(let elapsed):
        pausedSection(elapsed: elapsed)

      case .stopping:
        HStack {
          ProgressView()
            .scaleEffect(0.8)
          Text("Finalizing track assets...")
            .symairaText(.callout)
        }

      case .failed(let message):
        VStack(spacing: 12) {
          Text("Recording Failed")
            .symairaText(.subheading, respectsForeground: false)
            .foregroundColor(.red)
          Text(message)
            .symairaText(.caption, respectsForeground: false)
            .foregroundColor(.secondary)

          Button("Dismiss") {
            model.resetToIdle()
          }
        }

      case .completed(let meetingID):
        VStack(spacing: 12) {
          Text("Recording Complete")
            .symairaText(.subheading, respectsForeground: false)
            .foregroundColor(.green)
          Text("Meeting artifact was successfully stored.")
            .symairaText(.caption, respectsForeground: false)
            .foregroundColor(.secondary)

          HStack(spacing: 8) {
            Button("Reveal in Finder") {
              let layout = ArtifactLayout(dataRoot: SymMeetPaths().dataDirectory)
              let meetingDir = layout.meetingDirectory(meetingID.uuidString.lowercased())
              NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: meetingDir.path)
            }

            Button("Dismiss") {
              model.resetToIdle()
            }
            .buttonStyle(.bordered)
          }
        }
      }

      Divider()

      // Version info
      HStack {
        Text("v\(BuildInfo.version)")
          .symairaText(.caption, respectsForeground: false)
          .foregroundColor(.secondary)
        Spacer()
        SettingsLink {
          Text("Settings")
        }
        .buttonStyle(.plain)
        .symairaText(.caption, respectsForeground: false)
        .foregroundColor(.secondary)
        Button("Quit Agent") {
          NSApplication.shared.terminate(nil)
        }
        .buttonStyle(.plain)
        .foregroundColor(.secondary)
      }
    }
    .padding()
    .frame(width: 280)
  }

  // MARK: - Update Section

  @ViewBuilder
  private var updateSection: some View {
    switch updateChecker.status {
    case .available(let release):
      HStack(spacing: 8) {
        Image(systemName: "arrow.up.circle.fill")
          .foregroundColor(.orange)
        VStack(alignment: .leading, spacing: 2) {
          Text("Update available")
            .symairaText(.caption)
          Text(release.tagName)
            .symairaText(.caption, respectsForeground: false)
            .foregroundColor(.secondary)
        }
        Spacer()
        if !release.assets.isEmpty {
          Button("Install") {
            Task { await updateChecker.install(release) }
          }
          .buttonStyle(.borderedProminent)
          .controlSize(.small)
        }
        Button("Skip") {
          updateChecker.skip(release)
        }
        .buttonStyle(.plain)
        .symairaText(.caption, respectsForeground: false)
        .foregroundColor(.secondary)
        Link(destination: URL(string: release.htmlURL)!) {
          Image(systemName: "arrow.up.forward.app")
            .foregroundColor(.accentColor)
        }
        .buttonStyle(.plain)
      }
      .padding(8)
      .background(Color.orange.opacity(0.1))
      .cornerRadius(6)

    case .installing(let progress):
      HStack(spacing: 8) {
        ProgressView(value: progress, total: 1)
          .frame(width: 80)
        Text("Installing update…")
          .symairaText(.caption, respectsForeground: false)
          .foregroundColor(.secondary)
      }
      .padding(8)

    case .readyToRelaunch:
      HStack(spacing: 8) {
        Image(systemName: "checkmark.circle.fill")
          .foregroundColor(.green)
        Text("Update installed — relaunch to finish")
          .symairaText(.caption, respectsForeground: false)
          .foregroundColor(.secondary)
      }
      .padding(8)

    case .error:
      HStack(spacing: 4) {
        Image(systemName: "exclamationmark.triangle")
          .foregroundColor(.secondary)
          .symairaText(.caption, respectsForeground: false)
        Text("Update check failed")
          .symairaText(.caption, respectsForeground: false)
          .foregroundColor(.secondary)
        Spacer()
        Button("Retry") {
          Task { await updateChecker.checkForUpdate(force: true) }
        }
        .buttonStyle(.plain)
        .symairaText(.caption, respectsForeground: false)
      }
      .padding(8)

    case .upToDate, .unknown, .skipped:
      EmptyView()
    }
  }

  // MARK: - Sections

  private var idleSection: some View {
    VStack(spacing: 12) {
      Text("Symaira Meet")
        .symairaText(.subheading, respectsForeground: false)
        .foregroundColor(.primary)

      TextField("Purpose of recording...", text: $purposeInput)
        .textFieldStyle(.symaira)
        .padding(.horizontal, 8)

      Button(action: {
        let purpose = purposeInput.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !purpose.isEmpty else { return }
        Task { await model.initiateRecording(purpose: purpose) }
      }) {
        Text("Record Meeting")
          .symairaText(.bodyEmphasized, respectsForeground: false)
          .frame(maxWidth: .infinity)
          .padding(.vertical, 8)
          .background(purposeInput.isEmpty ? Color.gray : Color.red)
          .foregroundColor(.white)
          .cornerRadius(8)
      }
      .buttonStyle(.plain)
      .disabled(purposeInput.isEmpty)
    }
  }

  // Shared state for the idle section text field
  @State private var purposeInput: String = ""

  private func recordingSection(elapsed: TimeInterval) -> some View {
    VStack(spacing: 12) {
      HStack {
        Circle()
          .fill(Color.red)
          .frame(width: 10, height: 10)
        Text("Recording Live")
          .symairaText(.subheading)
        Spacer()
        Text(formatTime(elapsed))
          .symairaText(.mono)
      }

      HStack(spacing: 12) {
        Button(action: {
          Task {
            await model.pause()
          }
        }) {
          Text("Pause")
            .frame(maxWidth: .infinity)
            .padding(.vertical, 6)
            .background(Color.blue)
            .foregroundColor(.white)
            .cornerRadius(6)
        }
        .buttonStyle(.plain)

        Button(action: {
          Task {
            await model.stop()
          }
        }) {
          Text("Stop")
            .frame(maxWidth: .infinity)
            .padding(.vertical, 6)
            .background(Color.red)
            .foregroundColor(.white)
            .cornerRadius(6)
        }
        .buttonStyle(.plain)
      }
    }
  }

  private func pausedSection(elapsed: TimeInterval) -> some View {
    VStack(spacing: 12) {
      HStack {
        Circle()
          .fill(Color.gray)
          .frame(width: 10, height: 10)
        Text("Paused")
          .symairaText(.subheading)
        Spacer()
        Text(formatTime(elapsed))
          .symairaText(.mono)
      }

      HStack(spacing: 12) {
        Button(action: {
          Task {
            await model.resume()
          }
        }) {
          Text("Resume")
            .frame(maxWidth: .infinity)
            .padding(.vertical, 6)
            .background(Color.green)
            .foregroundColor(.white)
            .cornerRadius(6)
        }
        .buttonStyle(.plain)

        Button(action: {
          Task {
            await model.stop()
          }
        }) {
          Text("Stop")
            .frame(maxWidth: .infinity)
            .padding(.vertical, 6)
            .background(Color.red)
            .foregroundColor(.white)
            .cornerRadius(6)
        }
        .buttonStyle(.plain)
      }
    }
  }

  private func formatTime(_ time: TimeInterval) -> String {
    let hours = Int(time) / 3600
    let minutes = (Int(time) % 3600) / 60
    let seconds = Int(time) % 60
    if hours > 0 {
      return String(format: "%02d:%02d:%02d", hours, minutes, seconds)
    } else {
      return String(format: "%02d:%02d", minutes, seconds)
    }
  }
}
