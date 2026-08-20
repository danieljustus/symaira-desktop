import Foundation

public enum RetentionExecutionState: String, Codable, Equatable, Sendable {
  case completed
  case failed
  case notDue = "not_due"
}

public struct RetentionExecution: Codable, Equatable, Sendable {
  public let state: RetentionExecutionState
  public let removedArtifacts: [String]
  public let remainingArtifacts: [String]

  public init(
    state: RetentionExecutionState,
    removedArtifacts: [String] = [],
    remainingArtifacts: [String] = []
  ) {
    self.state = state
    self.removedArtifacts = removedArtifacts
    self.remainingArtifacts = remainingArtifacts
  }
}

/// Executes retention cleanup. Partial failures are reported, leave the model
/// job state intact, and can be retried safely.
public actor RetentionExecutor {
  public typealias RemoveItem = @Sendable (URL) throws -> Void

  private let store: MeetingStore
  private let removeItem: RemoveItem

  public init(
    store: MeetingStore,
    removeItem: @escaping RemoveItem = { url in try FileManager.default.removeItem(at: url) }
  ) {
    self.store = store
    self.removeItem = removeItem
  }

  public func execute(
    policy: RetentionPolicy,
    meetingID: String,
    exportState: RetentionExportState = .notExported,
    now: Date = Date()
  ) async throws -> RetentionExecution {
    guard policy.isDue(at: now, exportState: exportState) else {
      return RetentionExecution(state: .notDue)
    }

    let manifest = try await store.load(meetingID: meetingID)
    let artifacts = try await store.derivedArtifactURLs(meetingID: meetingID)
    var removed: [String] = []
    var remaining: [String] = []

    for artifact in artifacts where FileManager.default.fileExists(atPath: artifact.path) {
      do {
        try removeItem(artifact)
        removed.append(artifact.lastPathComponent)
      } catch {
        remaining.append(artifact.lastPathComponent)
      }
    }

    guard remaining.isEmpty else {
      return RetentionExecution(
        state: .failed, removedArtifacts: removed, remainingArtifacts: remaining)
    }

    var updatedManifest = manifest
    updatedManifest.job = nil
    do {
      try await store.update(updatedManifest)
      return RetentionExecution(state: .completed, removedArtifacts: removed)
    } catch {
      return RetentionExecution(
        state: .failed,
        removedArtifacts: removed,
        remainingArtifacts: [ArtifactLayout.manifestFile]
      )
    }
  }

  @discardableResult
  public func permanentlyDelete(
    meetingID: String,
    confirmation: PermanentDeletionConfirmation
  ) async throws -> Bool {
    switch confirmation {
    case .commandLine, .reviewedUI:
      try await store.permanentlyDelete(meetingID: meetingID)
    }
  }
}
