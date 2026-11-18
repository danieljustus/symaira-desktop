import Foundation

/// Decides whether a finished dictation may still be inserted.
///
/// Transcription is asynchronous: by the time text is ready the user may have started another
/// dictation, switched documents or already received this very result. Each session gets a
/// monotonically increasing generation bound to the document it started in; a result is
/// delivered only if it belongs to the newest session, the same document, and has not been
/// delivered before ("records delivery to prevent stale replay").
public struct DeliveryLedger: Equatable, Sendable {
  public private(set) var currentGeneration: UInt64 = 0
  private var currentDocumentID: String?
  private var delivered = false

  public init() {}

  /// Starts a session and returns its generation, invalidating all earlier ones.
  /// `documentID` identifies the insertion target (e.g. window/field identity, or the host
  /// document); pass `nil` where the platform cannot tell documents apart.
  @discardableResult
  public mutating func beginSession(documentID: String?) -> UInt64 {
    currentGeneration += 1
    currentDocumentID = documentID
    delivered = false
    return currentGeneration
  }

  /// Returns `true` once for the current generation and matching document.
  ///
  /// A document mismatch does not consume the delivery: the caller may fall back to another
  /// path (e.g. clipboard) or the user may return to the document.
  public mutating func shouldDeliver(generation: UInt64, documentID: String?) -> Bool {
    guard generation != 0, generation == currentGeneration, !delivered,
      documentID == currentDocumentID
    else { return false }
    delivered = true
    return true
  }

  /// Drops the current session (e.g. cancellation) so its late result is never delivered.
  public mutating func invalidateCurrent() {
    currentGeneration += 1
    delivered = true
  }
}

/// Guards restoring the user's previous pasteboard content after a paste-based insertion.
///
/// Dictation tools temporarily put the text on the pasteboard, paste, and then restore the old
/// content. Restoring is only safe if nothing else touched the pasteboard in between; a newer
/// user copy must never be overwritten.
public enum ClipboardRestorePolicy {
  /// - Parameters:
  ///   - recordedChangeCount: pasteboard change count right after our own write.
  ///   - currentChangeCount: change count now.
  ///   - generation: generation of the session that wrote to the pasteboard.
  ///   - currentGeneration: newest generation known to the ledger.
  public static func shouldRestore(
    recordedChangeCount: Int,
    currentChangeCount: Int,
    generation: UInt64,
    currentGeneration: UInt64
  ) -> Bool {
    recordedChangeCount == currentChangeCount && generation == currentGeneration
  }
}
