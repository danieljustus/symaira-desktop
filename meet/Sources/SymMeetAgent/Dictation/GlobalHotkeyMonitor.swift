import ApplicationServices
import CoreGraphics
import Foundation
import SymDictationCore

/// System-wide key observation through a CGEventTap.
///
/// An active tap (not listen-only) is used so Esc can be consumed while a dictation runs; it
/// requires the Accessibility permission. Without it `start()` returns `false` and nothing
/// else happens: no crash, no partial state.
@MainActor
final class GlobalHotkeyMonitor {
  var mapper: HotkeyEventMapper
  /// Gesture events produced from key events.
  var onGestureEvent: ((DictationGestureEvent) -> Void)?
  /// Whether a dictation is recording or processing (Esc is only ours then).
  var isDictationActive: () -> Bool = { false }

  private var tap: CFMachPort?
  private var source: CFRunLoopSource?

  init(hotkey: DictationHotkey) {
    mapper = HotkeyEventMapper(hotkey: hotkey)
  }

  var isRunning: Bool { tap != nil }

  func setHotkey(_ hotkey: DictationHotkey) {
    mapper = HotkeyEventMapper(hotkey: hotkey)
  }

  /// Installs the tap. Returns `false` if the system refuses (missing permission).
  @discardableResult
  func start() -> Bool {
    guard tap == nil else { return true }
    let mask: CGEventMask =
      (1 << CGEventType.flagsChanged.rawValue) | (1 << CGEventType.keyDown.rawValue)
      | (1 << CGEventType.keyUp.rawValue)
    let callback: CGEventTapCallBack = { _, type, event, userInfo in
      guard let userInfo else { return Unmanaged.passUnretained(event) }
      let monitor = Unmanaged<GlobalHotkeyMonitor>.fromOpaque(userInfo).takeUnretainedValue()
      // The tap's run loop source lives on the main run loop, so this runs on the main thread.
      let swallow = MainActor.assumeIsolated { monitor.handle(type: type, event: event) }
      return swallow ? nil : Unmanaged.passUnretained(event)
    }
    guard
      let tap = CGEvent.tapCreate(
        tap: .cgSessionEventTap, place: .headInsertEventTap, options: .defaultTap,
        eventsOfInterest: mask, callback: callback,
        userInfo: Unmanaged.passUnretained(self).toOpaque())
    else { return false }
    let source = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, tap, 0)
    CFRunLoopAddSource(CFRunLoopGetMain(), source, .commonModes)
    CGEvent.tapEnable(tap: tap, enable: true)
    self.tap = tap
    self.source = source
    return true
  }

  func stop() {
    if let tap { CGEvent.tapEnable(tap: tap, enable: false) }
    if let source { CFRunLoopRemoveSource(CFRunLoopGetMain(), source, .commonModes) }
    tap = nil
    source = nil
    mapper.reset()
  }

  /// Returns `true` if the event must not reach the focused application.
  private func handle(type: CGEventType, event: CGEvent) -> Bool {
    switch type {
    case .tapDisabledByTimeout, .tapDisabledByUserInput:
      // Key-ups may have been missed: forget held keys, abort a running recording, re-arm.
      mapper.reset()
      if isDictationActive() { onGestureEvent?(.escape) }
      if let tap { CGEvent.tapEnable(tap: tap, enable: true) }
      return false
    default:
      break
    }
    let now = ProcessInfo.processInfo.systemUptime
    let keyCode = UInt16(truncatingIfNeeded: event.getIntegerValueField(.keyboardEventKeycode))
    let raw: RawKeyEvent
    switch type {
    case .flagsChanged:
      raw = .flagsChanged(keyCode: keyCode, flags: event.flags.rawValue, time: now)
    case .keyDown:
      let isRepeat = event.getIntegerValueField(.keyboardEventAutorepeat) != 0
      raw = .keyDown(keyCode: keyCode, isRepeat: isRepeat, time: now)
    case .keyUp:
      raw = .keyUp(keyCode: keyCode, time: now)
    default:
      return false
    }
    let mapping = mapper.map(raw, dictationActive: isDictationActive())
    for gesture in mapping.events { onGestureEvent?(gesture) }
    return mapping.swallow
  }
}
