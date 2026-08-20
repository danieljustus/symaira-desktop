import ArgumentParser
import Foundation
import SymMeetCore
import SymMeetWhisperKit

extension SymMeet {
  struct Job: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "job",
      abstract: "Inspect and manage transcription jobs.",
      subcommands: [JobList.self, JobShow.self, JobCancel.self, JobRetry.self]
    )
  }

  struct JobList: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "list", abstract: "List transcription jobs.")

    @Option(name: .long, help: "Filter by job status.")
    var state: String?

    @Flag(name: .long, help: "Emit one machine-readable job list.")
    var json = false

    mutating func run() async throws {
      do {
        let paths = SymMeetPaths()
        let coordinator = JobCoordinator(dataRoot: paths.dataDirectory)
        var result = try await coordinator.list()

        if let stateFilter = state,
          let status = JobStatus(rawValue: stateFilter)
        {
          result = JobListResult(
            jobs: result.jobs.filter { $0.status == status },
            diagnostics: result.diagnostics)
        }

        if json {
          try Output.writeJSON(JobListOutput(result: result))
        } else if result.jobs.isEmpty {
          Output.writeLine("No jobs found.")
        } else {
          for job in result.jobs {
            let id = job.meetingID.uuidString.lowercased()
            Output.writeLine("\(id)\t\(job.status.rawValue)\tattempt=\(job.attempt)")
          }
          for diagnostic in result.diagnostics {
            Output.writeError(
              "Warning: \(diagnostic.meetingID) — \(diagnostic.code.rawValue)")
          }
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  struct JobShow: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "show", abstract: "Show one transcription job.")

    @Argument(help: "The meeting UUID or job UUID.")
    var jobID: String

    @Flag(name: .long, help: "Emit one machine-readable job document.")
    var json = false

    mutating func run() async throws {
      do {
        let paths = SymMeetPaths()
        let coordinator = JobCoordinator(dataRoot: paths.dataDirectory)
        let job = try await resolveJob(coordinator: coordinator, identifier: jobID, isJSON: json)

        if json {
          try Output.writeJSON(job)
        } else {
          Output.writeLine("Job: \(job.jobID.uuidString.lowercased())")
          Output.writeLine("Meeting: \(job.meetingID.uuidString.lowercased())")
          Output.writeLine("Status: \(job.status.rawValue)")
          Output.writeLine("Attempt: \(job.attempt)")
          if let engine = job.engine {
            Output.writeLine("Engine: \(engine.engineID) / \(engine.modelID)")
          }
          if !job.failureHistory.isEmpty {
            Output.writeLine("Failures: \(job.failureHistory.count)")
            for failure in job.failureHistory {
              let cls = failure.classification.rawValue
              Output.writeError("  attempt \(failure.attempt): [\(cls)] \(failure.message)")
            }
          }
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  struct JobCancel: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "cancel",
      abstract: "Request cooperative cancellation of a transcription job.")

    @Argument(help: "The meeting UUID or job UUID.")
    var jobID: String

    @Flag(name: .long, help: "Emit one machine-readable result document.")
    var json = false

    mutating func run() async throws {
      do {
        let paths = SymMeetPaths()
        let coordinator = JobCoordinator(dataRoot: paths.dataDirectory)
        let job = try await resolveJob(coordinator: coordinator, identifier: jobID, isJSON: json)

        guard job.status == .queued || job.status.isActive else {
          let message = "Job is already in terminal state: \(job.status.rawValue)."
          if json {
            try Output.writeJSON(
              JobMutationOutput(
                meetingID: job.meetingID.uuidString.lowercased(),
                status: job.status.rawValue,
                message: message))
          } else {
            Output.writeLine(message)
          }
          return
        }

        let handle = try await coordinator.lock.acquire(meetingID: job.meetingID)
        if job.status == .queued {
          // No in-flight write to wait for: cancel immediately.
          _ = try await coordinator.advance(
            meetingID: job.meetingID, to: .cancelled, using: handle,
            note: "cancelled before starting")
        } else {
          _ = try await coordinator.requestCancellation(meetingID: job.meetingID, using: handle)
          _ = try await coordinator.confirmCancelled(meetingID: job.meetingID, using: handle)
        }

        if json {
          try Output.writeJSON(
            JobMutationOutput(
              meetingID: job.meetingID.uuidString.lowercased(),
              status: "cancelled",
              message: "Job cancelled."))
        } else {
          Output.writeLine("Job cancelled.")
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  struct JobRetry: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "retry",
      abstract: "Retry a failed, cancelled, or interrupted transcription job.")

    @Argument(help: "The meeting UUID or job UUID.")
    var jobID: String

    @Flag(name: .long, help: "Emit one machine-readable result document.")
    var json = false

    mutating func run() async throws {
      do {
        let paths = SymMeetPaths()
        let coordinator = JobCoordinator(dataRoot: paths.dataDirectory)
        let job = try await resolveJob(coordinator: coordinator, identifier: jobID, isJSON: json)

        guard [.failed, .cancelled, .interrupted].contains(job.status) else {
          throw CLIError(
            exitCode: CLIExit.usage.rawValue,
            code: "job_not_found",
            message: "Job is not in a retryable state (current: \(job.status.rawValue)).",
            isJSON: json)
        }

        guard let engineProvenance = job.engine else {
          throw CLIError(
            exitCode: CLIExit.runtimeFailure.rawValue,
            code: "job_cannot_cancel",
            message: "Job has no engine provenance; cannot retry.",
            isJSON: json)
        }

        let modelStore = ModelStore()
        let record: ModelRecord
        do {
          record = try await modelStore.verify(id: engineProvenance.modelID)
        } catch ModelError.modelNotInstalled {
          let id = engineProvenance.modelID
          throw CLIError(
            exitCode: CLIExit.runtimeFailure.rawValue,
            code: "job_not_found",
            message: "Model '\(id)' is not installed. Run: symmeet model install \(id)",
            isJSON: json)
        }

        let pipeline = TranscriptionPipeline(dataRoot: paths.dataDirectory)
        let engine = try await WhisperKitEngine(
          modelID: engineProvenance.modelID, modelStore: modelStore)

        let signalSource = DispatchSource.makeSignalSource(signal: SIGINT)
        signal(SIGINT, SIG_IGN)
        signalSource.setEventHandler(handler: {
          Task { @Sendable in await pipeline.requestCancellation() }
        })
        signalSource.resume()
        defer { signalSource.cancel() }

        let tracker = ProgressTracker()
        let outcome = try await pipeline.retry(
          meetingID: job.meetingID,
          engine: engine,
          modelID: record.descriptor.id,
          modelVersion: record.descriptor.upstreamRevision,
          onProgress: { progress in
            switch progress {
            case .meetingCreated:
              break
            case .phase(let phase):
              Output.writeError("Phase: \(phase.rawValue)")
            case .progress(let value):
              tracker.update(value)
            case .segmentFinalized:
              break
            case .warning(let warning):
              Output.writeError("Warning [\(warning.code)]: \(warning.message)")
            }
          })

        if json {
          try Output.writeJSON(RetryOutput(outcome: outcome))
        } else {
          Output.writeLine(
            "Retried: \(outcome.segmentCount) segment(s) (\(outcome.language ?? "auto")).")
          Output.writeLine("Meeting: \(outcome.meetingID.uuidString.lowercased())")
          Output.writeLine("Status: \(outcome.status.rawValue)")
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }
}

// MARK: - Shared helpers

/// Resolves a job by trying the identifier as a meeting ID first, then
/// scanning all jobs for a matching job ID.
private func resolveJob(
  coordinator: JobCoordinator, identifier: String, isJSON: Bool
) async throws -> TranscriptionJob
{
  // Try as a meeting UUID first (the most common lookup path).
  if let meetingID = UUID(uuidString: identifier) {
    do {
      return try await coordinator.load(meetingID: meetingID)
    } catch JobError.notFound {
      // Fall through to scan by job ID.
    }
  }

  // Scan all jobs for a matching job ID.
  let result = try await coordinator.list()
  if let job = result.jobs.first(where: {
    $0.jobID.uuidString.lowercased() == identifier.lowercased()
      || $0.jobID.uuidString == identifier
  }) {
    return job
  }

  throw CLIError(
    exitCode: CLIExit.usage.rawValue,
    code: "job_operation_failed",
    message: "No job found for identifier: \(identifier)",
    isJSON: isJSON)
}

// MARK: - Output types

private struct JobListOutput: Encodable {
  let jobs: [TranscriptionJob]
  let diagnostics: [JobDiagnostic]

  init(result: JobListResult) {
    jobs = result.jobs
    diagnostics = result.diagnostics
  }
}

private struct JobMutationOutput: Encodable {
  let meetingID: String
  let status: String
  let message: String
}

private struct RetryOutput: Encodable {
  let meetingID: String
  let jobID: String
  let state: String
  let attempt: Int
  let segmentCount: Int
  let language: String?
  let engineID: String
  let modelID: String
  let sourceHash: String

  init(outcome: TranscriptionOutcome) {
    meetingID = outcome.meetingID.uuidString.lowercased()
    jobID = outcome.jobID.uuidString.lowercased()
    state = outcome.status.rawValue
    attempt = outcome.attempt
    segmentCount = outcome.segmentCount
    language = outcome.language
    engineID = outcome.engineID
    modelID = outcome.modelID
    sourceHash = outcome.sourceHash
  }
}

private final class ProgressTracker: @unchecked Sendable {
  private let lock = NSLock()
  private var lastPercent = -1

  func update(_ value: Double) {
    let percent = Int(value * 100)
    lock.lock()
    let shouldEmit = percent > lastPercent
    if shouldEmit { lastPercent = percent }
    lock.unlock()
    if shouldEmit {
      Output.writeError("Progress: \(percent)%")
    }
  }
}
