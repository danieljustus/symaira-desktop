import Foundation

public enum RetentionPolicy: Equatable, Sendable {
  case keep
  case deleteAfter(Date)
  case deleteAfterExport
}

public enum RetentionExportState: Equatable, Sendable {
  case notExported
  case exported
}

extension RetentionPolicy {
  func isDue(at now: Date, exportState: RetentionExportState) -> Bool {
    switch self {
    case .keep: false
    case .deleteAfter(let date): now >= date
    case .deleteAfterExport: exportState == .exported
    }
  }
}

/// An explicit user action is required before a trashed meeting is destroyed.
public enum PermanentDeletionConfirmation: Sendable {
  case commandLine
  case reviewedUI
}
