import Foundation
import SymairaUpdateCheck
import Testing
@testable import SymMeetAgent

struct AgentUpdateCheckerTests {
  @Test
  @MainActor
  func autoUpdatePreferencesPersistAcrossCheckerInstances() {
    let suiteName = "AgentUpdateCheckerTests.\(UUID().uuidString)"
    let defaults = UserDefaults(suiteName: suiteName)!
    defer { defaults.removePersistentDomain(forName: suiteName) }

    let keyPrefix = "tests.symmeet.agent.\(UUID().uuidString)"
    let skippedKey = "\(keyPrefix).skipped"
    let preferences = UserDefaultsAutoUpdatePreferenceStore(
      keyPrefix: keyPrefix,
      defaults: defaults
    )
    let skipped = UserDefaultsSkippedVersionStore(key: skippedKey, defaults: defaults)

    let initial = AgentUpdateChecker(
      store: skipped,
      autoPrefs: preferences,
      currentVersion: { "0.0.0" }
    )
    #expect(initial.autoCheckEnabled == false)
    #expect(initial.autoInstallEnabled == false)

    initial.autoCheckEnabled = true
    initial.autoInstallEnabled = true

    let recreatedPreferences = UserDefaultsAutoUpdatePreferenceStore(
      keyPrefix: keyPrefix,
      defaults: defaults
    )
    let recreated = AgentUpdateChecker(
      store: skipped,
      autoPrefs: recreatedPreferences,
      currentVersion: { "0.0.0" }
    )

    #expect(recreated.autoCheckEnabled)
    #expect(recreated.autoInstallEnabled)
  }
}
