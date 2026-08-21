import Foundation
import Observation
import SymroomKit

/// Owns the module's data flow: renders `symroom`'s --json output, never
/// reimplements room logic.
@Observable
@MainActor
public final class RoomAppState {
    public let client = RoomCLIClient()

    /// Absolute path of the currently selected room directory, or nil.
    public private(set) var roomDirectory: String?
    public private(set) var snapshot: RoomSnapshot?
    public private(set) var isLoading = false
    public private(set) var lastError: String?

    public init() {}

    public var isInstalled: Bool { client.isInstalled }

    public func select(roomDirectory: String) async {
        self.roomDirectory = roomDirectory
        await refresh()
    }

    public func refresh() async {
        guard let roomDirectory else { return }
        isLoading = true
        lastError = nil
        defer { isLoading = false }
        do {
            async let members = client.listMembers(in: roomDirectory)
            async let journal = client.journal(in: roomDirectory)
            async let pending = client.pendingRuns(in: roomDirectory)
            snapshot = try await RoomSnapshot(
                members: members,
                journal: journal,
                pendingRuns: pending
            )
        } catch {
            lastError = error.localizedDescription
            snapshot = nil
        }
    }

    /// Approve a pending run through the CLI (produces the same signed event
    /// as `symroom run approve`).
    public func approve(runID: String, scope: String?, ttl: String?) async -> String? {
        guard let roomDirectory else { return nil }
        do {
            let out = try await client.approve(runID: runID, scope: scope, ttl: ttl, in: roomDirectory)
            await refresh()
            return out
        } catch {
            lastError = error.localizedDescription
            return nil
        }
    }

    public func deny(runID: String, reason: String) async -> String? {
        guard let roomDirectory else { return nil }
        do {
            let out = try await client.deny(runID: runID, reason: reason, in: roomDirectory)
            await refresh()
            return out
        } catch {
            lastError = error.localizedDescription
            return nil
        }
    }
}
