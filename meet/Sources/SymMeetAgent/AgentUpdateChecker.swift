import Foundation
import SymairaUpdateCheck

/// The update state exposed to the SymMeetAgent views.
public enum AgentUpdateStatus: Equatable, Sendable {
  case unknown
  case upToDate
  case available(ReleaseInfo)
  case skipped(ReleaseInfo)
  case installing(progress: Double)
  case readyToRelaunch
  case error(String)
}

/// High-level update integration for the SymMeetAgent.
///
/// The appkit checker owns release caching and skipped-version persistence. This
/// adapter adds the app-specific preference store and the safe bundle installer
/// while keeping the UI independent from the low-level update implementation.
@MainActor
public final class AgentUpdateChecker: ObservableObject {
  public static let shared = AgentUpdateChecker()

  private static let skippedVersionKey = "dev.symaira.symmeet.agent.updateSkippedTag"
  private static let preferencesKey = "dev.symaira.symmeet.agent"

  @Published public private(set) var status: AgentUpdateStatus = .unknown

  private let appkitChecker: SymairaUpdateCheck.AppUpdateChecker
  private let applier: UpdateApplier

  public var autoCheckEnabled: Bool {
    get { autoPrefs.autoCheckEnabled }
    set {
      autoPrefs.autoCheckEnabled = newValue
      objectWillChange.send()
    }
  }

  public var autoInstallEnabled: Bool {
    get { autoPrefs.autoInstallEnabled }
    set {
      autoPrefs.autoInstallEnabled = newValue
      objectWillChange.send()
    }
  }

  private var autoPrefs: UserDefaultsAutoUpdatePreferenceStore

  public init(
    checker: UpdateChecker = UpdateChecker(owner: "danieljustus", repo: "symaira-meet"),
    store: UserDefaultsSkippedVersionStore? = nil,
    autoPrefs: UserDefaultsAutoUpdatePreferenceStore? = nil,
    currentVersion: @escaping () -> String = {
      Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "0.0.0"
    },
    applier: UpdateApplier = UpdateApplier(
      checkInstallMethod: true,
      binaryName: "SymMeetAgent"
    )
  ) {
    let selectedStore = store ?? UserDefaultsSkippedVersionStore(key: Self.skippedVersionKey)
    let selectedPrefs =
      autoPrefs
      ?? UserDefaultsAutoUpdatePreferenceStore(keyPrefix: Self.preferencesKey)

    self.autoPrefs = selectedPrefs
    self.applier = applier
    self.appkitChecker = SymairaUpdateCheck.AppUpdateChecker(
      checker: checker,
      store: selectedStore,
      currentVersion: currentVersion,
      autoPrefs: selectedPrefs
    )
  }

  /// Check for a newer release. `force` bypasses the appkit cache and skip gate.
  public func checkForUpdate(force: Bool = false) async {
    await appkitChecker.checkForUpdate(force: force)
    refreshStatus()
  }

  /// Run the cache-respecting launch check when enabled, then install only when
  /// the user explicitly enabled the matching auto-install preference.
  public func checkOnLaunchIfEnabled() async {
    await appkitChecker.checkOnLaunchIfEnabled()
    refreshStatus()

    if autoInstallEnabled, case .available(let release) = status {
      await install(release)
    }
  }

  /// Dismiss a specific release so the user is not re-prompted for it.
  public func skip(_ release: ReleaseInfo) {
    appkitChecker.skip(release)
    refreshStatus()
  }

  /// Download, verify, and install the selected release through appkit's
  /// install-method-safe bundle path. A successful install requires a relaunch.
  public func install(_ release: ReleaseInfo) async {
    status = .installing(progress: 0)
    do {
      _ = try await applier.applyBundle(
        release: release,
        targetPath: Bundle.main.bundlePath
      )
      status = .readyToRelaunch
    } catch {
      status = .error(String(describing: error))
    }
  }

  private func refreshStatus() {
    switch appkitChecker.status {
    case .unknown:
      status = .unknown
    case .upToDate:
      status = .upToDate
    case .available(let release):
      status = .available(release)
    case .skipped(let release):
      status = .skipped(release)
    case .installing(let progress):
      status = .installing(progress: progress)
    case .readyToRelaunch:
      status = .readyToRelaunch
    case .error(let message):
      status = .error(message)
    }
  }
}
