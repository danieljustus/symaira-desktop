import Foundation
import SymairaCLIRunner
import SymairaToolKit

/// Narrow, injectable contract for the operations `DeskCore` needs from
/// either a local `symdesk` CLI process or a remote SymDesk Server. Feature
/// methods on `DeskCore` call through this contract instead of branching on
/// "is this session local or remote" at every call site.
///
/// `vaultArgs` is accepted by the ingest/job methods (rather than baked into
/// the transport at construction time) because `DeskCore.vaultPath` can
/// change during a session; the remote implementations ignore it since a
/// server connection is already scoped to one vault.
public protocol DeskTransport: Sendable {
    /// Runs a command and returns its raw response bytes.
    func command(arguments: [String], stdin: String) async throws -> Data

    /// Streams a command's newline-delimited JSON output line by line.
    func commandStream(arguments: [String], stdin: String) -> AsyncThrowingStream<String, Error>

    /// Returns the raw Markdown content of a vault note.
    func fileContent(path: String) async throws -> String

    /// Writes new content for a vault note.
    func saveFile(path: String, content: String) async throws

    /// Ingests a local file into the vault, returning an implementation-defined identifier for the created item.
    func ingestFile(_ fileURL: URL, vaultArgs: [String]) async throws -> String

    /// Lists ingest jobs.
    func ingestJobs(vaultArgs: [String]) async throws -> [IngestJob]

    /// Retries a failed ingest job.
    func ingestRetry(jobID: String, vaultArgs: [String]) async throws
}

/// Executes the contract against a local `symdesk` CLI subprocess.
public struct LocalDeskTransport: DeskTransport {
    private let tool: DetectedTool
    private let runner: CLIRunner

    public init(tool: DetectedTool, runner: CLIRunner = CLIRunner()) {
        self.tool = tool
        self.runner = runner
    }

    public func command(arguments: [String], stdin: String) async throws -> Data {
        try await runner.runChecked(tool.location.url, arguments: arguments)
    }

    public func commandStream(arguments: [String], stdin: String) -> AsyncThrowingStream<String, Error> {
        AsyncThrowingStream { continuation in
            let process = Process()
            process.executableURL = tool.location.url
            process.arguments = arguments

            let task = Task {
                do {
                    let outPipe = Pipe()
                    process.standardOutput = outPipe

                    if !stdin.isEmpty {
                        let inPipe = Pipe()
                        process.standardInput = inPipe
                        try process.run()
                        if let data = stdin.data(using: .utf8) {
                            inPipe.fileHandleForWriting.write(data)
                        }
                        try? inPipe.fileHandleForWriting.close()
                    } else {
                        try process.run()
                    }

                    for try await line in outPipe.fileHandleForReading.bytes.lines {
                        try Task.checkCancellation()
                        continuation.yield(line)
                    }
                    process.waitUntilExit()
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            // Mirrors RemoteDeskTransport: cancelling the stream (e.g. the
            // view disappearing) tears down the underlying work instead of
            // leaving an orphaned subprocess running.
            continuation.onTermination = { _ in
                task.cancel()
                if process.isRunning {
                    process.terminate()
                }
            }
        }
    }

    public func fileContent(path: String) async throws -> String {
        guard let data = FileManager.default.contents(atPath: path) else { return "" }
        return String(decoding: data, as: UTF8.self)
    }

    public func saveFile(path: String, content: String) async throws {
        guard let data = content.data(using: .utf8) else { return }
        try data.write(to: URL(fileURLWithPath: path), options: .atomic)
    }

    public func ingestFile(_ fileURL: URL, vaultArgs: [String]) async throws -> String {
        struct IngestRes: Codable, Sendable { let path: String }
        let res = try await runner.runDecoding(
            IngestRes.self,
            executable: tool.location.url,
            arguments: ["ingest", fileURL.path, "--json"] + vaultArgs
        )
        return res.path
    }

    public func ingestJobs(vaultArgs: [String]) async throws -> [IngestJob] {
        // Not `runner.runDecoding`: IngestJob's custom `init(from:)` already
        // maps its own explicit snake_case CodingKeys, and layering
        // `.convertFromSnakeCase` on top of that double-transforms every
        // multi-word key (source_path, document_id, ...) into a lookup
        // miss. Match RemoteDeskTransport's plain-decoder approach instead.
        let data = try await runner.runChecked(
            tool.location.url,
            arguments: ["ingest", "jobs", "--json"] + vaultArgs
        )
        do {
            return try JSONDecoder().decode([IngestJob].self, from: data)
        } catch {
            throw CLIRunnerError.invalidJSON(description: String(describing: error))
        }
    }

    public func ingestRetry(jobID: String, vaultArgs: [String]) async throws {
        _ = try await runner.runChecked(
            tool.location.url,
            arguments: ["ingest", "retry", "\(jobID)"] + vaultArgs
        )
    }
}

/// Executes the contract against a remote SymDesk Server over `/api/v1`.
public struct RemoteDeskTransport: DeskTransport {
    private let client: RemoteDeskClient

    public init(client: RemoteDeskClient) {
        self.client = client
    }

    public func command(arguments: [String], stdin: String) async throws -> Data {
        try await client.command(arguments: arguments, stdin: stdin)
    }

    public func commandStream(arguments: [String], stdin: String) -> AsyncThrowingStream<String, Error> {
        client.commandStream(arguments: arguments, stdin: stdin)
    }

    public func fileContent(path: String) async throws -> String {
        try await client.noteContent(path: path)
    }

    public func saveFile(path: String, content: String) async throws {
        try await client.saveNote(path: path, content: content)
    }

    public func ingestFile(_ fileURL: URL, vaultArgs: [String]) async throws -> String {
        try await client.ingest(fileURL: fileURL)
    }

    public func ingestJobs(vaultArgs: [String]) async throws -> [IngestJob] {
        let data = try await client.jobs()
        return try JSONDecoder().decode([IngestJob].self, from: data)
    }

    public func ingestRetry(jobID: String, vaultArgs: [String]) async throws {
        try await client.retryJob(id: jobID)
    }
}
