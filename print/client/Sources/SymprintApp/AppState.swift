import Foundation
import Observation

/// Shared application state that routes OS-level events — files opened from
/// Finder ("Open With", double-click, Dock drag, `open -a`) — into the
/// currently visible views.
@Observable
@MainActor
final class AppState {
    static let shared = AppState()

    /// The most recent document URL handed to the app from outside.
    /// Views observe this value and load the document when it changes.
    private(set) var pendingDocumentURL: URL?

    func handleOpen(_ url: URL) {
        pendingDocumentURL = url
    }

    /// Consumes the pending open event. Called once the target view has loaded
    /// the document, so a later tab switch does not replay the same open.
    func consumePendingDocument() {
        pendingDocumentURL = nil
    }
}
