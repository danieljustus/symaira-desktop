import Combine
import Foundation
import SymDictationCore

/// Which speech engine dictation uses.
enum DictationEngineChoice: String, CaseIterable, Identifiable {
  case parakeet
  case whisperKit

  var id: String { rawValue }

  var title: String {
    switch self {
    case .parakeet: return "Parakeet Ultra (empfohlen)"
    case .whisperKit: return "WhisperKit (Rückfall)"
    }
  }
}

/// Persisted dictation preferences. Off by default; nothing here records history or audio.
@MainActor
final class DictationSettings: ObservableObject {
  static let shared = DictationSettings()

  private enum Key {
    static let enabled = "dictation.enabled"
    static let hotkey = "dictation.hotkey"
    static let alwaysType = "dictation.alwaysType"
    static let vaultPath = "dictation.vaultPath"
    static let engine = "dictation.engine"
  }

  private let defaults: UserDefaults

  @Published var isEnabled: Bool {
    didSet { defaults.set(isEnabled, forKey: Key.enabled) }
  }
  @Published var hotkey: DictationHotkey {
    didSet { defaults.set(hotkey.rawValue, forKey: Key.hotkey) }
  }
  /// Type text instead of pasting in every app (Chromium and Electron targets always type).
  @Published var alwaysType: Bool {
    didSet { defaults.set(alwaysType, forKey: Key.alwaysType) }
  }
  /// Document vault holding `.symdesk/dictation/*.md`; empty means "use SYMDESK_VAULT".
  @Published var vaultPath: String {
    didSet { defaults.set(vaultPath, forKey: Key.vaultPath) }
  }
  @Published var engine: DictationEngineChoice {
    didSet { defaults.set(engine.rawValue, forKey: Key.engine) }
  }

  init(defaults: UserDefaults = .standard) {
    self.defaults = defaults
    isEnabled = defaults.bool(forKey: Key.enabled)
    hotkey = defaults.string(forKey: Key.hotkey).flatMap(DictationHotkey.init) ?? .fn
    alwaysType = defaults.bool(forKey: Key.alwaysType)
    vaultPath = defaults.string(forKey: Key.vaultPath) ?? ""
    engine = defaults.string(forKey: Key.engine).flatMap(DictationEngineChoice.init) ?? .parakeet
  }

  /// The vault to read vocabulary from: the setting wins, then `SYMDESK_VAULT`.
  func vaultLoader(environment: [String: String] = ProcessInfo.processInfo.environment)
    -> DictationVaultLoader
  {
    let trimmed = vaultPath.trimmingCharacters(in: .whitespacesAndNewlines)
    if !trimmed.isEmpty {
      return DictationVaultLoader(
        vaultURL: URL(fileURLWithPath: (trimmed as NSString).expandingTildeInPath))
    }
    return DictationVaultLoader(environment: environment)
  }
}

extension DictationHotkey {
  var title: String {
    switch self {
    case .fn: return "Fn / Globe-Taste"
    case .rightOption: return "Rechte Wahltaste"
    }
  }
}
