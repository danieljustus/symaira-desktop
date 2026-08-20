import ArgumentParser
import Darwin
import Foundation
import SymMeetCore

extension SymMeet {
  struct Doctor: AsyncParsableCommand {
    static let configuration = CommandConfiguration(abstract: "Inspect local symmeet readiness.")

    @Flag(name: .long, help: "Emit one machine-readable diagnostic document.")
    var json = false

    mutating func run() async throws {
      let report = DoctorReport(paths: SymMeetPaths())
      if json {
        try Output.writeJSON(report)
      } else {
        Output.writeLine("OS: \(report.os)")
        Output.writeLine("Architecture: \(report.architecture)")
        for (label, key) in [
          ("Artifact store", "artifact_store"),
          ("Capture", "capture"),
          ("Models", "models"),
        ] {
          guard let check = report.checks[key] else { continue }
          Output.writeLine("\(label): \(check.status.rawValue)")
          if let remediation = check.remediation {
            Output.writeLine("  Remediation: \(remediation)")
          }
        }
      }

      if !report.allOK {
        // The report has already been emitted. Exiting directly keeps --json
        // machine-readable instead of appending a second error document.
        Darwin.exit(CLIExit.runtimeFailure.rawValue)
      }
    }
  }
}

private enum CheckStatus: String, Codable {
  case ok
  case warn
  case fail
}

private struct DoctorReport: Encodable {
  let os: String
  let architecture: String
  let paths: DoctorPaths
  let disk: DiskReport
  let checks: [String: DoctorCheck]

  var allOK: Bool {
    !checks.values.contains { $0.status == .fail }
  }

  init(paths: SymMeetPaths) {
    os = ProcessInfo.processInfo.operatingSystemVersionString
    architecture = Self.machineArchitecture()
    self.paths = DoctorPaths(paths: paths)
    disk = DiskReport(root: paths.dataDirectory)

    let dataExists = FileManager.default.fileExists(atPath: paths.dataDirectory.path)
    let writable = Self.isWritable(paths.dataDirectory)
    let artifactStore: DoctorCheck
    if dataExists {
      artifactStore = DoctorCheck(
        status: writable ? .ok : .fail,
        remediation: writable
          ? nil
          : "Set XDG_DATA_HOME to a writable location, then run doctor again.")
    } else {
      artifactStore = DoctorCheck(
        status: .warn,
        remediation:
          "Create the data directory before recording, or set XDG_DATA_HOME to a writable location.")
    }

    checks = [
      "artifact_store": artifactStore,
      "capture": DoctorCheck(
        status: .warn,
        remediation: "Grant Screen Recording and Microphone access to symmeet in System Settings."),
      "models": DoctorCheck(
        status: .warn,
        remediation: "Install a transcription model with `symmeet model install <id>` before processing."),
    ]
  }

  private static func isWritable(_ url: URL) -> Bool {
    var candidate = url
    while !FileManager.default.fileExists(atPath: candidate.path), candidate.path != "/" {
      candidate.deleteLastPathComponent()
    }
    return FileManager.default.isWritableFile(atPath: candidate.path)
  }

  private static func machineArchitecture() -> String {
    var system = utsname()
    uname(&system)
    return withUnsafePointer(to: &system.machine) { pointer in
      pointer.withMemoryRebound(to: CChar.self, capacity: 1) { String(cString: $0) }
    }
  }
}

private struct DoctorPaths: Encodable {
  let config: PathStatus
  let cache: PathStatus
  let data: PathStatus

  init(paths: SymMeetPaths) {
    config = PathStatus(url: paths.configFile)
    cache = PathStatus(url: paths.workDirectory.deletingLastPathComponent())
    data = PathStatus(url: paths.dataDirectory)
  }
}

private struct PathStatus: Encodable {
  let path: String
  let exists: Bool
  let writable: Bool

  init(url: URL) {
    path = url.path
    exists = FileManager.default.fileExists(atPath: url.path)
    var candidate = url
    while !FileManager.default.fileExists(atPath: candidate.path), candidate.path != "/" {
      candidate.deleteLastPathComponent()
    }
    writable = FileManager.default.isWritableFile(atPath: candidate.path)
  }
}

private struct DiskReport: Encodable {
  let availableBytes: Int64?

  init(root: URL) {
    var candidate = root
    while !FileManager.default.fileExists(atPath: candidate.path), candidate.path != "/" {
      candidate.deleteLastPathComponent()
    }
    let capacity = try? candidate.resourceValues(
      forKeys: [.volumeAvailableCapacityForImportantUsageKey]
    ).volumeAvailableCapacityForImportantUsage
    if let capacity {
      // Reporting whole gibibytes makes the diagnostic stable across ordinary
      // command invocations while retaining an actionable free-space signal.
      availableBytes = capacity / (1_024 * 1_024 * 1_024) * (1_024 * 1_024 * 1_024)
    } else {
      availableBytes = nil
    }
  }
}

private struct DoctorCheck: Encodable {
  let status: CheckStatus
  let remediation: String?
}
