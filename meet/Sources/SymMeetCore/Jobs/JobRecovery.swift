import Foundation

/// The outcome of a recovery sweep.
public struct JobRecoveryReport: Equatable, Sendable {
  /// Jobs that were found active with no live lock owner and were moved to
  /// `interrupted`.
  public let recovered: [TranscriptionJob]
  /// Job records that could not be read while listing.
  public let diagnostics: [JobDiagnostic]

  public init(recovered: [TranscriptionJob], diagnostics: [JobDiagnostic]) {
    self.recovered = recovered
    self.diagnostics = diagnostics
  }
}

/// Restart recovery for the durable job store.
///
/// A sweep claims the data-root lock itself before touching anything. If
/// that fails because a live process already holds it, recovery is a no-op:
/// per the "one active mutating process per data root" invariant, a live
/// owner means every job artifact is already legitimately spoken for and
/// nothing here can be abandoned. If the lock is unheld or verifiably stale,
/// acquiring it succeeds (``DataRootLock`` only ever breaks a lock after
/// positively confirming the previous owner is gone -- never from file age
/// alone, and never across a still-live PID from a different boot/session).
/// Once held, every job still in an active status (`preparing`,
/// `transcribing`, `exporting`, `cancelling`) is, by construction, abandoned
/// and is converted to `interrupted` -- never silently resumed.
public actor JobRecovery {
  private let coordinator: JobCoordinator

  public init(dataRoot: URL) {
    coordinator = JobCoordinator(dataRoot: dataRoot)
  }

  public init(coordinator: JobCoordinator) {
    self.coordinator = coordinator
  }

  @discardableResult
  public func recoverAbandonedJobs() async throws -> JobRecoveryReport {
    let handle: LockHandle
    do {
      handle = try await coordinator.lock.acquire()
    } catch JobError.lockHeld {
      return JobRecoveryReport(recovered: [], diagnostics: [])
    }

    do {
      let listing = try await coordinator.list()
      var recovered: [TranscriptionJob] = []
      for job in listing.jobs where job.status.isActive {
        let updated = try await coordinator.markInterrupted(
          meetingID: job.meetingID,
          using: handle,
          reason: "recovered: no live lock owner claimed this job")
        recovered.append(updated)
      }
      try? await coordinator.lock.release(handle)
      return JobRecoveryReport(recovered: recovered, diagnostics: listing.diagnostics)
    } catch {
      try? await coordinator.lock.release(handle)
      throw error
    }
  }
}
