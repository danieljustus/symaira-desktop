import Foundation

// MARK: - Plan

/// What is known about the control that currently has keyboard focus.
public enum FocusedElementState: Equatable, Sendable {
  /// An editable text control (text field, text area, combo box, web text input).
  case textInput
  /// Something that definitely cannot take text (button, list, desktop).
  case notTextInput
  /// The accessibility query gave no answer. Chromium and Electron only expose web content
  /// to accessibility on demand, so "unknown" must not be treated as "no text field".
  case unknown
}

/// The app that would receive the text, as observed when the result is ready.
public struct InsertionTarget: Equatable, Sendable {
  public var bundleID: String?
  /// The app bundles the Electron framework (detected by the agent from the bundle).
  public var isElectronApp: Bool
  public var focus: FocusedElementState
  /// Identity of the insertion target for `DeliveryLedger`; `nil` where unknown.
  public var documentID: String?

  public init(
    bundleID: String? = nil,
    isElectronApp: Bool = false,
    focus: FocusedElementState = .unknown,
    documentID: String? = nil
  ) {
    self.bundleID = bundleID
    self.isElectronApp = isElectronApp
    self.focus = focus
    self.documentID = documentID
  }
}

/// System state that overrides any per-app choice.
public struct InsertionConditions: Equatable, Sendable {
  /// `IsSecureEventInputEnabled()`: a password field (or another secure consumer) is active.
  public var secureInputActive: Bool
  /// The process may post key events (Accessibility permission).
  public var accessibilityTrusted: Bool

  public init(secureInputActive: Bool = false, accessibilityTrusted: Bool = true) {
    self.secureInputActive = secureInputActive
    self.accessibilityTrusted = accessibilityTrusted
  }
}

public enum InsertionMethod: Equatable, Sendable {
  /// Put the text on the pasteboard and post Cmd+V, restoring the pasteboard afterwards.
  case paste
  /// Post the text as unicode key events. Never touches the pasteboard.
  case type
}

/// Why the text is held in a card instead of being inserted.
public enum CardReason: Equatable, Sendable {
  /// Secure Input is on: neither insert nor touch the pasteboard.
  case secureInput
  /// No Accessibility permission, so no key events can be posted.
  case accessibilityMissing
  /// The focused control cannot take text.
  case noTextField
  /// The user switched to another app or document while the text was being prepared.
  case documentChanged
  /// Posting the events failed.
  case insertFailed
}

public enum InsertionPlan: Equatable, Sendable {
  case insert(InsertionMethod)
  case card(CardReason)
}

/// Chooses between paste, typing and a card. Pure function of its inputs.
public struct InsertionPolicy: Equatable, Sendable {
  /// Bundle identifiers of Chromium-based browsers where paste is unreliable.
  public static let chromiumBundleIDs: Set<String> = [
    "com.google.Chrome", "com.google.Chrome.beta", "com.google.Chrome.dev",
    "com.google.Chrome.canary", "org.chromium.Chromium", "com.brave.Browser",
    "com.brave.Browser.beta", "com.brave.Browser.nightly", "com.microsoft.edgemac",
    "com.microsoft.edgemac.Beta", "com.microsoft.edgemac.Dev", "com.microsoft.edgemac.Canary",
    "company.thebrowser.Browser", "com.operasoftware.Opera", "com.vivaldi.Vivaldi",
  ]

  /// Use typing for every target (configurable fallback).
  public var alwaysType: Bool
  /// Additional bundle identifiers that should be typed into.
  public var extraTypingBundleIDs: Set<String>

  public init(alwaysType: Bool = false, extraTypingBundleIDs: Set<String> = []) {
    self.alwaysType = alwaysType
    self.extraTypingBundleIDs = extraTypingBundleIDs
  }

  public func prefersTyping(_ target: InsertionTarget) -> Bool {
    if alwaysType || target.isElectronApp { return true }
    guard let bundleID = target.bundleID else { return false }
    return Self.chromiumBundleIDs.contains(bundleID) || extraTypingBundleIDs.contains(bundleID)
  }

  public func plan(for target: InsertionTarget, conditions: InsertionConditions) -> InsertionPlan {
    if conditions.secureInputActive { return .card(.secureInput) }
    if !conditions.accessibilityTrusted { return .card(.accessibilityMissing) }
    if target.focus == .notTextInput { return .card(.noTextField) }
    return .insert(prefersTyping(target) ? .type : .paste)
  }
}

// MARK: - Platform seams

/// Everything on the pasteboard, as raw data per item, so it can be put back unchanged.
public struct ClipboardSnapshot: Equatable, Sendable {
  public var items: [[String: Data]]

  public init(items: [[String: Data]] = []) {
    self.items = items
  }
}

@MainActor
public protocol DictationPasteboard: AnyObject {
  var changeCount: Int { get }
  func snapshot() -> ClipboardSnapshot
  /// Replaces the content with `text` and returns the change count right after our own write.
  func write(text: String) -> Int
  func restore(_ snapshot: ClipboardSnapshot)
}

@MainActor
public protocol DictationKeyboard: AnyObject {
  /// Posts Cmd+V to the focused application.
  func postPaste() throws
  /// Posts `text` as unicode key events.
  func type(_ text: String) throws
}

// MARK: - Delivery

public enum InsertionOutcome: Equatable, Sendable {
  case inserted(InsertionMethod)
  /// Keep the text visible for the user; nothing was inserted or put on the pasteboard.
  case card(CardReason)
  /// The session was cancelled or superseded; the text is dropped on purpose.
  case discarded
}

/// Delivers a finished dictation and keeps the user's pasteboard intact.
///
/// Restore rules: the pasteboard is put back only if it still holds our own write
/// (`changeCount` unchanged) and no newer session started. If a second paste starts while the
/// first one's restore is pending, the original snapshot is carried over so the user's real
/// clipboard, not our previous dictation, is what finally comes back.
@MainActor
public final class DictationInserter {
  public var policy: InsertionPolicy

  private let pasteboard: any DictationPasteboard
  private let keyboard: any DictationKeyboard
  private let restoreDelay: TimeInterval
  private let sleep: @MainActor (TimeInterval) async -> Void
  private var ledger = DeliveryLedger()
  private var sessionDocumentID: String?
  private var deliveredGeneration: UInt64 = 0
  private var pendingRestore: (snapshot: ClipboardSnapshot, writtenChangeCount: Int)?
  private var pastesInFlight = 0
  private var abandonedWhileInFlight = false

  public init(
    policy: InsertionPolicy = InsertionPolicy(),
    pasteboard: any DictationPasteboard,
    keyboard: any DictationKeyboard,
    restoreDelay: TimeInterval = 0.4,
    sleep: @escaping @MainActor (TimeInterval) async -> Void = { seconds in
      try? await Task.sleep(nanoseconds: UInt64(max(0, seconds) * 1_000_000_000))
    }
  ) {
    self.policy = policy
    self.pasteboard = pasteboard
    self.keyboard = keyboard
    self.restoreDelay = restoreDelay
    self.sleep = sleep
  }

  /// Starts a session and returns its generation, invalidating every earlier one.
  @discardableResult
  public func beginSession(documentID: String?) -> UInt64 {
    sessionDocumentID = documentID
    return ledger.beginSession(documentID: documentID)
  }

  /// Drops the current session so its late result is never delivered.
  public func cancelSession() {
    ledger.invalidateCurrent()
  }

  /// A newer session ended without pasting (it was cancelled or produced no text): put the
  /// user's clipboard back if it still holds our text. While a paste is still in flight the
  /// request is remembered and applied when that paste finishes, so the target application
  /// never loses the text it is about to read.
  public func restoreAbandonedPaste() {
    if pastesInFlight > 0 {
      abandonedWhileInFlight = true
      return
    }
    restorePendingIfUntouched()
  }

  private func restorePendingIfUntouched() {
    guard let pending = pendingRestore, pending.writtenChangeCount == pasteboard.changeCount
    else { return }
    pasteboard.restore(pending.snapshot)
    pendingRestore = nil
  }

  public func deliver(
    _ text: String,
    generation: UInt64,
    target: InsertionTarget,
    conditions: InsertionConditions
  ) async -> InsertionOutcome {
    guard ledger.shouldDeliver(generation: generation, documentID: target.documentID) else {
      // Same undelivered session but another document: keep the text. Anything else (older
      // generation, cancelled, already delivered) is stale and dropped.
      let wrongDocument =
        generation == ledger.currentGeneration && generation != deliveredGeneration
        && target.documentID != sessionDocumentID
      return wrongDocument ? .card(.documentChanged) : .discarded
    }
    deliveredGeneration = generation
    switch policy.plan(for: target, conditions: conditions) {
    case .card(let reason):
      return .card(reason)
    case .insert(.type):
      do {
        try keyboard.type(text)
        return .inserted(.type)
      } catch {
        return .card(.insertFailed)
      }
    case .insert(.paste):
      return await paste(text, generation: generation)
    }
  }

  private func paste(_ text: String, generation: UInt64) async -> InsertionOutcome {
    let original: ClipboardSnapshot
    if let pending = pendingRestore, pending.writtenChangeCount == pasteboard.changeCount {
      original = pending.snapshot
    } else {
      original = pasteboard.snapshot()
    }
    let written = pasteboard.write(text: text)
    pendingRestore = (original, written)
    pastesInFlight += 1
    defer { pastesInFlight -= 1 }

    do {
      try keyboard.postPaste()
    } catch {
      restoreIfOwn(original, written: written, generation: generation)
      return .card(.insertFailed)
    }
    await sleep(restoreDelay)
    restoreIfOwn(original, written: written, generation: generation)
    if abandonedWhileInFlight, pastesInFlight == 1 {
      abandonedWhileInFlight = false
      restorePendingIfUntouched()
    }
    return .inserted(.paste)
  }

  private func restoreIfOwn(_ snapshot: ClipboardSnapshot, written: Int, generation: UInt64) {
    let restorable = ClipboardRestorePolicy.shouldRestore(
      recordedChangeCount: written,
      currentChangeCount: pasteboard.changeCount,
      generation: generation,
      currentGeneration: ledger.currentGeneration)
    if restorable {
      pasteboard.restore(snapshot)
      pendingRestore = nil
    } else if pendingRestore?.writtenChangeCount == written, pasteboard.changeCount != written {
      // Someone else copied in the meantime: their content wins and our snapshot is obsolete.
      pendingRestore = nil
    }
  }
}
