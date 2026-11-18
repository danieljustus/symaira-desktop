import Foundation
import SymDictationCore
import Testing

@testable import SymMeetAgent

@Suite("DictationSettingsTests")
@MainActor
struct DictationSettingsTests {
  private func makeDefaults() -> UserDefaults {
    let name = "dictation-settings-\(UUID().uuidString)"
    let defaults = UserDefaults(suiteName: name)!
    defaults.removePersistentDomain(forName: name)
    return defaults
  }

  @Test("Dictation is off by default with Fn and Parakeet")
  func defaults() {
    let settings = DictationSettings(defaults: makeDefaults())
    #expect(settings.isEnabled == false)
    #expect(settings.hotkey == .fn)
    #expect(settings.engine == .parakeet)
    #expect(settings.alwaysType == false)
    #expect(settings.vaultPath.isEmpty)
  }

  @Test("Changes persist across instances")
  func persistence() {
    let defaults = makeDefaults()
    let first = DictationSettings(defaults: defaults)
    first.hotkey = .rightOption
    first.isEnabled = true
    first.alwaysType = true
    first.engine = .whisperKit
    first.vaultPath = "~/Vault"

    let second = DictationSettings(defaults: defaults)
    #expect(second.hotkey == .rightOption)
    #expect(second.isEnabled)
    #expect(second.alwaysType)
    #expect(second.engine == .whisperKit)
    #expect(second.vaultPath == "~/Vault")
  }

  @Test("Configured vault wins over SYMDESK_VAULT, which is the fallback")
  func vaultResolution() {
    let settings = DictationSettings(defaults: makeDefaults())
    #expect(settings.vaultLoader(environment: [:]).vaultURL == nil)
    #expect(
      settings.vaultLoader(environment: ["SYMDESK_VAULT": "/env/vault"]).vaultURL?.path
        == "/env/vault")
    settings.vaultPath = "/setting/vault"
    #expect(
      settings.vaultLoader(environment: ["SYMDESK_VAULT": "/env/vault"]).vaultURL?.path
        == "/setting/vault")
  }

  @Test("German hints contain no dashes used as punctuation")
  func germanHintsHaveNoDashes() {
    let reasons: [CardReason] = [
      .secureInput, .accessibilityMissing, .noTextField, .documentChanged, .insertFailed,
    ]
    let notices: [DictationNotice] = [
      .microphoneUnavailable("x"), .transcriptionFailed("x"), .nothingHeard,
    ]
    let texts =
      reasons.map(DictationHints.cardTitle(for:)) + notices.map(DictationHints.message(for:))
    for text in texts {
      #expect(!text.contains("\u{2014}"), "em dash in: \(text)")
      #expect(!text.contains("\u{2013}"), "en dash in: \(text)")
      #expect(!text.isEmpty)
    }
  }
}
