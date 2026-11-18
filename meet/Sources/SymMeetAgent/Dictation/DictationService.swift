import AVFoundation
import AppKit
import ApplicationServices
import Combine
import Foundation
import SymDictationCore
import SymMeetCapture

/// Owns push-to-talk dictation inside the agent: permissions, model, hotkey and controller.
///
/// Dictation is opt-in. While it is off nothing runs: no event tap, no engine, no microphone.
/// Nothing in this class is reachable from `SymMeetMCP`; only the local user's settings switch
/// it on.
@MainActor
final class DictationService: ObservableObject {
  enum Status: Equatable {
    case off
    case needsMicrophone
    case preparingModel(progress: Double)
    case needsAccessibility
    case ready
    case failed(String)
  }

  static let shared = DictationService(settings: .shared)

  @Published private(set) var status: Status = .off

  private let settings: DictationSettings
  private let permissionService: CapturePermissionService
  private let presenter = DictationPanelPresenter()
  private let recorder = DictationMicRecorder()
  private let environment = SystemDictationEnvironment()
  private let pasteboard = SystemDictationPasteboard()
  private let keyboard = CGEventDictationKeyboard()
  private lazy var inserter = DictationInserter(
    policy: InsertionPolicy(alwaysType: settings.alwaysType),
    pasteboard: pasteboard, keyboard: keyboard)

  private var monitor: GlobalHotkeyMonitor?
  private var controller: DictationController?
  private var transcriber: (any DictationTranscriber)?
  private var loadedEngine: DictationEngineChoice?
  private var startTask: Task<Void, Never>?
  private var accessibilityPoll: Task<Void, Never>?
  private var cancellables: Set<AnyCancellable> = []

  init(
    settings: DictationSettings,
    permissionService: CapturePermissionService = CapturePermissionService()
  ) {
    self.settings = settings
    self.permissionService = permissionService
    settings.$hotkey
      .dropFirst()
      .sink { [weak self] hotkey in self?.monitor?.setHotkey(hotkey) }
      .store(in: &cancellables)
    settings.$alwaysType
      .dropFirst()
      .sink { [weak self] value in self?.inserter.policy.alwaysType = value }
      .store(in: &cancellables)
    settings.$engine
      .dropFirst()
      .sink { [weak self] _ in
        guard let self, self.settings.isEnabled else { return }
        self.restart()
      }
      .store(in: &cancellables)
  }

  /// Called on launch: resume dictation only if the user switched it on earlier.
  func resumeIfEnabled() {
    if settings.isEnabled { restart() }
  }

  func setEnabled(_ enabled: Bool) {
    settings.isEnabled = enabled
    if enabled { restart() } else { stop() }
  }

  /// Re-checks permissions after the user came back from System Settings.
  func recheck() {
    if settings.isEnabled, status != .ready { restart() }
  }

  /// Shows the system prompt for Accessibility and opens the matching settings pane.
  func requestAccessibility() {
    let options = ["AXTrustedCheckOptionPrompt": true] as CFDictionary
    _ = AXIsProcessTrustedWithOptions(options)
  }

  func openPrivacySettings(pane: String) {
    let url = "x-apple.systempreferences:com.apple.preference.security?\(pane)"
    if let url = URL(string: url) { NSWorkspace.shared.open(url) }
  }

  // MARK: - Lifecycle

  private func restart() {
    stop(keepSetting: true)
    startTask = Task { [weak self] in await self?.start() }
  }

  private func stop(keepSetting: Bool = false) {
    startTask?.cancel()
    startTask = nil
    accessibilityPoll?.cancel()
    accessibilityPoll = nil
    monitor?.stop()
    monitor = nil
    controller?.shutdown()
    controller = nil
    presenter.tearDown()
    if !keepSetting {
      transcriber = nil
      loadedEngine = nil
      status = .off
    }
  }

  private func start() async {
    // 1. Microphone: a prompt on first use, a clear status if refused.
    var micStatus = AVCaptureDevice.authorizationStatus(for: .audio)
    if micStatus == .notDetermined {
      _ = await permissionService.requestMicrophoneAccess()
      micStatus = AVCaptureDevice.authorizationStatus(for: .audio)
    }
    guard micStatus == .authorized else {
      status = .needsMicrophone
      return
    }

    // 2. Model: downloaded on first enable, with visible progress.
    if transcriber == nil || loadedEngine != settings.engine {
      status = .preparingModel(progress: 0)
      do {
        let loaded = try await DictationEngineLoader.load(settings.engine) { [weak self] value in
          Task { @MainActor in
            guard let self, case .preparingModel = self.status else { return }
            self.status = .preparingModel(progress: value)
          }
        }
        guard !Task.isCancelled else { return }
        transcriber = loaded
        loadedEngine = settings.engine
      } catch {
        guard !Task.isCancelled else { return }
        status = .failed(error.localizedDescription)
        return
      }
    }
    guard !Task.isCancelled, let transcriber else { return }

    // 3. Accessibility: needed for the key tap and for inserting text.
    guard installHotkey(transcriber: transcriber) else {
      status = .needsAccessibility
      pollForAccessibility()
      return
    }
    status = .ready
  }

  private func installHotkey(transcriber: any DictationTranscriber) -> Bool {
    let monitor = GlobalHotkeyMonitor(hotkey: settings.hotkey)
    let controller = DictationController(
      recorder: recorder,
      transcriber: transcriber,
      pipelineProvider: { [settings] in
        // Re-read the vault per dictation so edits to the Markdown files apply immediately.
        DictationPipeline(vocabulary: settings.vaultLoader().loadVocabulary().value)
      },
      environment: environment,
      inserter: inserter,
      presenter: presenter)
    monitor.isDictationActive = { [weak controller] in controller?.isActive ?? false }
    monitor.onGestureEvent = { [weak controller] event in controller?.handle(event) }
    guard monitor.start() else { return false }
    self.monitor = monitor
    self.controller = controller
    return true
  }

  private func pollForAccessibility() {
    accessibilityPoll?.cancel()
    accessibilityPoll = Task { [weak self] in
      while !Task.isCancelled {
        try? await Task.sleep(nanoseconds: 1_500_000_000)
        guard let self, !Task.isCancelled else { return }
        if AXIsProcessTrusted(), let transcriber = self.transcriber,
          self.installHotkey(transcriber: transcriber)
        {
          self.status = .ready
          self.accessibilityPoll = nil
          return
        }
      }
    }
  }
}
