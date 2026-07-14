import SwiftUI

/// Tracks per-item progress and failure state for retryable async mutations.
///
/// Call sites use this so a failed destructive or recoverable action leaves
/// its target item selected/visible and available for retry, instead of the
/// caller silently discarding the error and advancing local UI state as if
/// the mutation had succeeded.
@MainActor
final class AsyncActionTracker<ID: Hashable>: ObservableObject {
    @Published private(set) var inFlight: Set<ID> = []
    @Published private(set) var failures: [ID: String] = [:]

    func isInFlight(_ id: ID) -> Bool {
        inFlight.contains(id)
    }

    func failureMessage(for id: ID) -> String? {
        failures[id]
    }

    /// Runs `operation` for `id`, de-duplicating concurrent calls for the
    /// same id. Returns `true` only when `operation` completes without
    /// throwing; callers should gate any local state change (clearing a
    /// selection, dismissing a banner, refreshing a list) on that result.
    @discardableResult
    func run(_ id: ID, operation: () async throws -> Void) async -> Bool {
        guard !inFlight.contains(id) else { return false }
        inFlight.insert(id)
        failures[id] = nil
        defer { inFlight.remove(id) }
        do {
            try await operation()
            return true
        } catch {
            failures[id] = error.localizedDescription
            return false
        }
    }

    func clearFailure(for id: ID) {
        failures[id] = nil
    }

    #if DEBUG
    /// Test-only hook to simulate an in-flight run without a real
    /// long-running operation, for de-duplication assertions.
    func testMarkInFlight(_ id: ID) {
        inFlight.insert(id)
    }
    #endif
}

extension View {
    /// Presents a retryable failure alert for a single id tracked by an
    /// `AsyncActionTracker`. Dismissing without retrying just clears the
    /// recorded failure so the item stays available for another attempt.
    func asyncActionAlert<ID: Hashable>(
        _ tracker: AsyncActionTracker<ID>,
        id: ID,
        title: String,
        retry: @escaping () -> Void
    ) -> some View {
        alert(title, isPresented: Binding(
            get: { tracker.failureMessage(for: id) != nil },
            set: { isPresented in
                if !isPresented { tracker.clearFailure(for: id) }
            }
        )) {
            Button("Retry") { retry() }
            Button("Dismiss", role: .cancel) { tracker.clearFailure(for: id) }
        } message: {
            Text(tracker.failureMessage(for: id) ?? "")
        }
    }
}
