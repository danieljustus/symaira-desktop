import AppKit
import SwiftUI
import SymMeetCore
import SymairaTheme
import SymairaUpdateCheck

/// Main window for the Symaira Meet desktop app.
///
/// Composes the recording controls (state machine lives in `AgentModel`),
/// permission handling, consent flow, and the local meeting history from
/// `MeetingStore`. The menu-bar extra stays available as a companion, but
/// this window is the primary surface of the app.
struct MainWindowView: View {
  @ObservedObject var model: AgentModel
  @ObservedObject var updateChecker: AgentUpdateChecker

  @State private var purposeInput: String = ""
  @State private var meetings: [MeetingManifest] = []
  @State private var storeDiagnostics: Int = 0

  var body: some View {
    ZStack {
      SymairaBackdrop(showsGrid: false)
        .ignoresSafeArea()

      VStack(spacing: 0) {
        header
        Divider().overlay(SymairaTheme.borderGlass)

        ScrollView {
          VStack(alignment: .leading, spacing: SymairaSpacing.large) {
            updateBanner
            recordingCard
            historySection
          }
          .padding(SymairaSpacing.large)
        }

        Divider().overlay(SymairaTheme.borderGlass)
        footer
      }
    }
    .frame(minWidth: 620, minHeight: 480)
    .task { await reloadMeetings() }
    .onChange(of: model.state) { _, newState in
      if case .completed = newState {
        Task { await reloadMeetings() }
      }
    }
  }

  // MARK: - Header

  private var header: some View {
    HStack(spacing: SymairaSpacing.medium) {
      Image(systemName: "waveform.circle.fill")
        .symairaText(.heading, respectsForeground: false)
        .foregroundStyle(SymairaTheme.goldPrimary)

      VStack(alignment: .leading, spacing: 2) {
        Text("Symaira Meet")
          .symairaText(.subheading, respectsForeground: false)
          .foregroundStyle(SymairaTheme.textPrimary)
        Text("Local-first meeting artifacts")
          .symairaText(.caption, respectsForeground: false)
          .foregroundStyle(SymairaTheme.textMuted)
      }

      Spacer()

      statusBadge
    }
    .padding(.horizontal, SymairaSpacing.large)
    .padding(.vertical, SymairaSpacing.medium)
  }

  @ViewBuilder
  private var statusBadge: some View {
    switch model.state {
    case .recording(let elapsed):
      SymairaBadge("Recording \(formatTime(elapsed))", tone: .critical, systemImage: "record.circle")
    case .paused(let elapsed):
      SymairaBadge("Paused \(formatTime(elapsed))", tone: .warning, systemImage: "pause.circle")
    case .starting, .stopping:
      SymairaBadge("Working…", tone: .informative, systemImage: "gearshape")
    case .failed:
      SymairaBadge("Failed", tone: .critical, systemImage: "exclamationmark.triangle")
    case .permissionRequired:
      SymairaBadge("Permissions missing", tone: .warning, systemImage: "lock.shield")
    default:
      SymairaBadge("Ready", tone: .positive, systemImage: "checkmark.circle")
    }
  }

  // MARK: - Update banner

  @ViewBuilder
  private var updateBanner: some View {
    switch updateChecker.status {
    case .available(let release):
      SymairaNotice(
        title: "Update available: \(release.tagName)",
        message: "A newer version of Symaira Meet has been released.",
        tone: .warning
      )
      HStack(spacing: SymairaSpacing.medium) {
        if !release.assets.isEmpty {
          Button("Install") {
            Task { await updateChecker.install(release) }
          }
          .buttonStyle(SymairaPrimaryButtonStyle())
        }
        if let url = URL(string: release.htmlURL) {
          Link("View release", destination: url)
            .symairaText(.caption)
        }
        Button("Skip") { updateChecker.skip(release) }
          .buttonStyle(.plain)
          .symairaText(.caption, respectsForeground: false)
          .foregroundStyle(SymairaTheme.textMuted)
      }
    case .installing(let progress):
      HStack(spacing: SymairaSpacing.small) {
        ProgressView(value: progress, total: 1)
          .frame(width: 140)
        Text("Installing update…")
          .symairaText(.caption)
      }
    case .readyToRelaunch:
      SymairaNotice(
        title: "Update installed",
        message: "Quit and relaunch Symaira Meet to start the new version.",
        tone: .positive
      )
    case .error(let message):
      SymairaNotice(
        title: "Update check failed",
        message: message,
        tone: .critical
      )
    default:
      EmptyView()
    }
  }

  // MARK: - Recording card

  private var recordingCard: some View {
    VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
      switch model.state {
      case .permissionRequired:
        PermissionView(model: model)

      case .consentConfirmation:
        consentSection

      case .starting:
        workingRow("Starting capture session…")

      case .recording(let elapsed):
        activeRecordingSection(elapsed: elapsed, paused: false)

      case .paused(let elapsed):
        activeRecordingSection(elapsed: elapsed, paused: true)

      case .stopping:
        workingRow("Finalizing track assets…")

      case .failed(let message):
        failedSection(message)

      case .completed(let meetingID):
        completedSection(meetingID)

      case .idle:
        idleSection
      }
    }
    .padding(SymairaSpacing.large)
    .modifier(GlassCardModifier())
  }

  private var idleSection: some View {
    VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
      Text("New recording")
        .symairaText(.bodyEmphasized, respectsForeground: false)
        .foregroundStyle(SymairaTheme.textPrimary)

      TextField("Purpose of the meeting…", text: $purposeInput)
        .textFieldStyle(.symaira)

      HStack {
        Text("Audio is captured and processed locally on this device.")
          .symairaText(.caption, respectsForeground: false)
          .foregroundStyle(SymairaTheme.textMuted)
        Spacer()
        Button {
          let purpose = purposeInput.trimmingCharacters(in: .whitespacesAndNewlines)
          guard !purpose.isEmpty else { return }
          Task { await model.initiateRecording(purpose: purpose) }
        } label: {
          Label("Record Meeting", systemImage: "record.circle")
        }
        .buttonStyle(SymairaPrimaryButtonStyle())
        .disabled(purposeInput.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
      }
    }
  }

  private var consentSection: some View {
    VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
      Text("Confirm consent")
        .symairaText(.bodyEmphasized, respectsForeground: false)
        .foregroundStyle(SymairaTheme.textPrimary)
      Text(
        "By proceeding, you attest that all participants have been informed "
          + "and you authorize this recording. It will be processed locally on your device."
      )
      .symairaText(.secondary, respectsForeground: false)
      .foregroundStyle(SymairaTheme.textSecondary)

      HStack {
        Spacer()
        Button("Cancel") { model.cancelConsent() }
          .buttonStyle(SymairaSecondaryButtonStyle())
        Button("Confirm & Start") {
          Task { await model.confirmConsent(attested: true) }
        }
        .buttonStyle(SymairaPrimaryButtonStyle())
      }
    }
  }

  private func workingRow(_ label: String) -> some View {
    HStack(spacing: SymairaSpacing.small) {
      ProgressView().scaleEffect(0.8)
      Text(label)
        .symairaText(.secondary, respectsForeground: false)
        .foregroundStyle(SymairaTheme.textSecondary)
    }
  }

  private func activeRecordingSection(elapsed: TimeInterval, paused: Bool) -> some View {
    VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
      HStack {
        Circle()
          .fill(paused ? SymairaTheme.warning : SymairaTheme.critical)
          .frame(width: 10, height: 10)
        Text(paused ? "Paused" : "Recording live")
          .symairaText(.bodyEmphasized, respectsForeground: false)
          .foregroundStyle(SymairaTheme.textPrimary)
        Spacer()
        Text(formatTime(elapsed))
          .symairaText(.mono, respectsForeground: false)
          .foregroundStyle(SymairaTheme.goldPrimary)
      }

      HStack(spacing: SymairaSpacing.small) {
        if paused {
          Button {
            Task { await model.resume() }
          } label: {
            Label("Resume", systemImage: "play.fill")
          }
          .buttonStyle(SymairaPrimaryButtonStyle())
        } else {
          Button {
            Task { await model.pause() }
          } label: {
            Label("Pause", systemImage: "pause.fill")
          }
          .buttonStyle(SymairaSecondaryButtonStyle())
        }

        Spacer()

        Button(role: .destructive) {
          Task { await model.stop() }
        } label: {
          Label("Stop & Save", systemImage: "stop.fill")
        }
        .buttonStyle(SymairaSecondaryButtonStyle())
      }
    }
  }

  private func failedSection(_ message: String) -> some View {
    VStack(alignment: .leading, spacing: SymairaSpacing.small) {
      SymairaNotice(
        title: "Recording failed",
        message: message,
        tone: .critical
      )
      HStack {
        Spacer()
        Button("Dismiss") { model.resetToIdle() }
          .buttonStyle(SymairaSecondaryButtonStyle())
      }
    }
  }

  private func completedSection(_ meetingID: UUID) -> some View {
    VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
      HStack(spacing: SymairaSpacing.small) {
        Image(systemName: "checkmark.circle.fill")
          .foregroundStyle(SymairaTheme.positive)
        Text("Recording complete — meeting artifact stored.")
          .symairaText(.bodyEmphasized, respectsForeground: false)
          .foregroundStyle(SymairaTheme.textPrimary)
      }
      HStack {
        Button("Reveal in Finder") {
          reveal(meetingID: meetingID.uuidString.lowercased())
        }
        .buttonStyle(SymairaSecondaryButtonStyle())
        Spacer()
        Button("New recording") { model.resetToIdle() }
          .buttonStyle(SymairaPrimaryButtonStyle())
      }
    }
  }

  // MARK: - History

  private var historySection: some View {
    VStack(alignment: .leading, spacing: SymairaSpacing.small) {
      HStack {
        Text("Meetings")
          .symairaText(.bodyEmphasized, respectsForeground: false)
          .foregroundStyle(SymairaTheme.textPrimary)
        if storeDiagnostics > 0 {
          SymairaBadge("\(storeDiagnostics) skipped", tone: .warning, systemImage: "exclamationmark.triangle")
        }
        Spacer()
        Button {
          Task { await reloadMeetings() }
        } label: {
          Image(systemName: "arrow.clockwise")
        }
        .buttonStyle(.plain)
        .foregroundStyle(SymairaTheme.textMuted)
        .help("Reload meeting history")
      }

      if meetings.isEmpty {
        SymairaEmptyState(
          systemImage: "tray",
          title: "No meetings yet",
          message: "Recorded meetings appear here as portable artifacts."
        )
        .frame(maxWidth: .infinity)
      } else {
        VStack(spacing: SymairaSpacing.xSmall) {
          ForEach(meetings, id: \.meetingID) { meeting in
            meetingRow(meeting)
          }
        }
      }
    }
  }

  private func meetingRow(_ meeting: MeetingManifest) -> some View {
    let id = meeting.meetingID.uuidString.lowercased()
    return HStack(spacing: SymairaSpacing.medium) {
      Image(systemName: "waveform")
        .foregroundStyle(SymairaTheme.goldPrimary)
        .frame(width: 20)

      VStack(alignment: .leading, spacing: 2) {
        Text(meeting.createdAt.formatted(date: .abbreviated, time: .shortened))
          .symairaText(.callout, respectsForeground: false)
          .foregroundStyle(SymairaTheme.textPrimary)
        Text(id)
          .symairaText(.monoSmall, respectsForeground: false)
          .foregroundStyle(SymairaTheme.textMuted)
          .lineLimit(1)
          .truncationMode(.middle)
      }

      Spacer()

      SymairaBadge(
        "\(meeting.audioTracks.count) tracks",
        tone: .neutral,
        systemImage: "music.note"
      )

      if meeting.consent.status == .authorized {
        SymairaBadge("Consented", tone: .positive, systemImage: "checkmark.shield")
      } else {
        SymairaBadge("Consent pending", tone: .warning, systemImage: "exclamationmark.shield")
      }

      Button {
        reveal(meetingID: id)
      } label: {
        Image(systemName: "folder")
      }
      .buttonStyle(.plain)
      .foregroundStyle(SymairaTheme.textSecondary)
      .help("Reveal in Finder")
    }
    .padding(.horizontal, SymairaSpacing.medium)
    .padding(.vertical, SymairaSpacing.small)
    .modifier(GlassCardModifier(cornerRadius: SymairaRadius.control))
  }

  // MARK: - Footer

  private var footer: some View {
    HStack {
      Text("v\(BuildInfo.version)")
        .symairaText(.caption, respectsForeground: false)
        .foregroundStyle(SymairaTheme.textMuted)
      Spacer()
      SettingsLink {
        Text("Settings")
      }
      .buttonStyle(.plain)
      .symairaText(.caption, respectsForeground: false)
      .foregroundStyle(SymairaTheme.textSecondary)
      Button("Open data folder") {
        let dir = SymMeetPaths().dataDirectory
        NSWorkspace.shared.open(dir)
      }
      .buttonStyle(.plain)
      .symairaText(.caption, respectsForeground: false)
      .foregroundStyle(SymairaTheme.textSecondary)
    }
    .padding(.horizontal, SymairaSpacing.large)
    .padding(.vertical, SymairaSpacing.small)
  }

  // MARK: - Helpers

  private func reloadMeetings() async {
    let store = MeetingStore()
    do {
      let result = try await store.list()
      meetings = result.meetings.sorted { $0.createdAt > $1.createdAt }
      storeDiagnostics = result.diagnostics.count
    } catch {
      meetings = []
      storeDiagnostics = 0
    }
  }

  private func reveal(meetingID: String) {
    let layout = ArtifactLayout(dataRoot: SymMeetPaths().dataDirectory)
    let dir = layout.meetingDirectory(meetingID)
    NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: dir.path)
  }

  private func formatTime(_ time: TimeInterval) -> String {
    let hours = Int(time) / 3600
    let minutes = (Int(time) % 3600) / 60
    let seconds = Int(time) % 60
    if hours > 0 {
      return String(format: "%02d:%02d:%02d", hours, minutes, seconds)
    }
    return String(format: "%02d:%02d", minutes, seconds)
  }
}
