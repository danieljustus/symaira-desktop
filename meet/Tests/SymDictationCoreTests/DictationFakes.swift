import Foundation

@testable import SymDictationCore

struct FakeKeyError: Error {}

@MainActor
final class FakePasteboard: DictationPasteboard {
  private(set) var changeCount = 0
  private(set) var content: ClipboardSnapshot
  private(set) var writes: [String] = []
  private(set) var restores = 0

  init(text: String? = "original") {
    content =
      text.map { ClipboardSnapshot(items: [["public.utf8-plain-text": Data($0.utf8)]]) }
      ?? ClipboardSnapshot()
  }

  var text: String? {
    content.items.first?["public.utf8-plain-text"].flatMap { String(data: $0, encoding: .utf8) }
  }

  func snapshot() -> ClipboardSnapshot { content }

  func write(text: String) -> Int {
    writes.append(text)
    content = ClipboardSnapshot(items: [["public.utf8-plain-text": Data(text.utf8)]])
    changeCount += 1
    return changeCount
  }

  func restore(_ snapshot: ClipboardSnapshot) {
    restores += 1
    content = snapshot
    changeCount += 1
  }

  /// The user (or another app) copies something.
  func foreignCopy(_ text: String) {
    content = ClipboardSnapshot(items: [["public.utf8-plain-text": Data(text.utf8)]])
    changeCount += 1
  }
}

@MainActor
final class FakeKeyboard: DictationKeyboard {
  private(set) var pastes = 0
  private(set) var typed: [String] = []
  var failPaste = false
  var failType = false
  /// Called when Cmd+V is posted, to observe the pasteboard at that moment.
  var onPaste: (() -> Void)?

  func postPaste() throws {
    if failPaste { throw FakeKeyError() }
    pastes += 1
    onPaste?()
  }

  func type(_ text: String) throws {
    if failType { throw FakeKeyError() }
    typed.append(text)
  }
}

/// A sleep whose wake-up the test controls, so ordering is deterministic.
@MainActor
final class SleepGates {
  private var waiters: [CheckedContinuation<Void, Never>] = []
  private(set) var requested = 0

  func sleep(_ seconds: TimeInterval) async {
    requested += 1
    await withCheckedContinuation { waiters.append($0) }
  }

  func releaseAll() {
    let pending = waiters
    waiters.removeAll()
    for waiter in pending { waiter.resume() }
  }

  func waitForSleepers(_ count: Int) async {
    while waiters.count < count { await Task.yield() }
  }
}
