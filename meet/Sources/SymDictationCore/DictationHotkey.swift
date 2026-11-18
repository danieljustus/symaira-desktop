import Foundation

/// The key that drives push-to-talk. Both keys are modifier keys and therefore arrive as
/// `flagsChanged` events rather than key-down/key-up pairs.
public enum DictationHotkey: String, CaseIterable, Codable, Sendable {
  /// Fn / Globe key. The system's own "Press 🌐 key to" action should be set to "Do Nothing".
  case fn
  /// Right Option key (the left one stays usable for typing special characters).
  case rightOption

  /// Hardware key code reported with the `flagsChanged` event.
  public var keyCode: UInt16 {
    switch self {
    case .fn: return 63
    case .rightOption: return 61
    }
  }

  /// Bit in the event flags that is set while this very key is down.
  ///
  /// Fn uses `NSEvent.ModifierFlags.function` (`maskSecondaryFn`). Right Option must use the
  /// device-dependent bit (`NX_DEVICERALTKEYMASK`) because the generic Option flag also stays
  /// set while the *left* Option key is held.
  public var pressedMask: UInt64 {
    switch self {
    case .fn: return 0x0080_0000
    case .rightOption: return 0x0000_0040
    }
  }
}

/// A keyboard event reduced to what dictation needs, independent of AppKit/CoreGraphics so the
/// mapping can be tested without posting or tapping real events.
public enum RawKeyEvent: Equatable, Sendable {
  case flagsChanged(keyCode: UInt16, flags: UInt64, time: TimeInterval)
  case keyDown(keyCode: UInt16, isRepeat: Bool, time: TimeInterval)
  case keyUp(keyCode: UInt16, time: TimeInterval)
}

/// Result of mapping one raw event.
public struct HotkeyMapping: Equatable, Sendable {
  public var events: [DictationGestureEvent]
  /// The event belongs to dictation and must not reach the focused application (Esc only).
  public var swallow: Bool

  public static let ignored = HotkeyMapping(events: [], swallow: false)

  public init(events: [DictationGestureEvent], swallow: Bool = false) {
    self.events = events
    self.swallow = swallow
  }
}

/// Turns raw key events into gesture events.
///
/// - The configured modifier key produces `keyDown`/`keyUp` on its press/release edges.
/// - Esc produces `.escape` and is swallowed, but only while dictation is active so Esc keeps
///   working everywhere else.
/// - Any other key pressed while the hotkey is held means the user is using the key as part of
///   a chord (for example Fn+Delete), not speaking: the recording is cancelled.
public struct HotkeyEventMapper: Equatable, Sendable {
  public static let escapeKeyCode: UInt16 = 53

  public var hotkey: DictationHotkey
  private var hotkeyDown = false
  private var swallowingEscapeRelease = false

  public init(hotkey: DictationHotkey = .fn) {
    self.hotkey = hotkey
  }

  /// - Parameter dictationActive: a recording or processing session is currently running.
  public mutating func map(_ event: RawKeyEvent, dictationActive: Bool) -> HotkeyMapping {
    switch event {
    case .flagsChanged(let keyCode, let flags, let time):
      guard keyCode == hotkey.keyCode else { return .ignored }
      let pressed = flags & hotkey.pressedMask != 0
      guard pressed != hotkeyDown else { return .ignored }
      hotkeyDown = pressed
      return HotkeyMapping(events: [pressed ? .keyDown(at: time) : .keyUp(at: time)])

    case .keyDown(let keyCode, let isRepeat, _):
      if keyCode == Self.escapeKeyCode {
        guard dictationActive else { return .ignored }
        swallowingEscapeRelease = true
        return isRepeat
          ? HotkeyMapping(events: [], swallow: true)
          : HotkeyMapping(events: [.escape], swallow: true)
      }
      if hotkeyDown, dictationActive, !isRepeat {
        return HotkeyMapping(events: [.escape])
      }
      return .ignored

    case .keyUp(let keyCode, _):
      if keyCode == Self.escapeKeyCode, swallowingEscapeRelease {
        swallowingEscapeRelease = false
        return HotkeyMapping(events: [], swallow: true)
      }
      return .ignored
    }
  }

  /// Forgets a held key, e.g. after the event tap was disabled by the system and key-up
  /// events may have been missed.
  public mutating func reset() {
    hotkeyDown = false
    swallowingEscapeRelease = false
  }
}
