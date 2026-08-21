import Foundation
import Observation
import SymairaToolKit
import SymairaDaemonKit

/// Performs the authenticated HTTP health check against the daemon's
/// `/status` endpoint.  Returns `true` only when the daemon answers
/// HTTP 200 with a non-empty body while carrying our
/// `Authorization: Bearer <token>` header.  A 401 (bad/missing token)
/// or any network error (connection refused, timeout, …) is NOT a
/// running daemon — this is the single source of truth for "is a daemon
/// already serving?", and it never consults the CLI or the local
/// database.
enum DaemonHealth {
    static func check(port: Int, token: String?, session: URLSession = .shared) async -> Bool {
        guard let url = URL(string: "http://127.0.0.1:\(port)/status") else { return false }
        var request = URLRequest(url: url)
        request.timeoutInterval = 1.5
        if let token {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        do {
            let (data, response) = try await session.data(for: request)
            guard let httpResponse = response as? HTTPURLResponse,
                  httpResponse.statusCode == 200,
                  !data.isEmpty else {
                return false
            }
            return true
        } catch {
            // No daemon on this port.
            return false
        }
    }
}

/// Manages the embedded symseek engine process on macOS.
@Observable
@MainActor
public final class EngineManager {
    public enum State: Sendable {
        case stopped
        case starting
        case running(port: Int)
        case failed(String)
    }

    public private(set) var state: State = .stopped
    public private(set) var logs: [String] = []

    public var isRunning: Bool {
        if case .running = state { return true }
        return false
    }

    public var port: Int? {
        if case .running(let p) = state { return p }
        return nil
    }

    /// Reads the daemon API token from the XDG config path
    /// (~/.config/symseek/api-token), if present. The daemon creates this
    /// file on first start; every client HTTP call to the daemon must send
    /// it as `Authorization: Bearer <token>`.
    public var apiToken: String? {
        let tokenURL = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".config/symseek/api-token")
        guard let data = FileManager.default.contents(atPath: tokenURL.path),
              let token = String(data: data, encoding: .utf8)?
                  .trimmingCharacters(in: .whitespacesAndNewlines),
              !token.isEmpty else {
            return nil
        }
        return token
    }

    private let supervisor = DaemonSupervisor()
    /// Dedicated serial queue for the supervisor stop path (issue #306).
    /// DaemonSupervisor.stop() historically blocked the calling thread on an
    /// internal mutex that was never released, freezing the app and forcing a
    /// force-quit when invoked from the main actor (0.7.0 replaced the raw
    /// pthread mutex with an NSRecursiveLock, but the stop path must still
    /// never run on the main thread — a lock held across a hop can deadlock
    /// the whole UI).  Serializing here also guarantees repeated stop() calls
    /// can never re-enter the supervisor concurrently.
    private let stopQueue = DispatchQueue(label: "dev.symaira.seek.engine-stop")
    private let maxLogs = 500
    private var currentPort: Int = 8080

    public init() {
        setupSupervisor()
    }

    private func setupSupervisor() {
        supervisor.onLog = { [weak self] logLine in
            Task { @MainActor [weak self] in
                self?.appendLog("[\(logLine.isError ? "stderr" : "stdout")] \(logLine.text)")
            }
        }
        supervisor.onStateChange = { [weak self] newState in
            Task { @MainActor [weak self] in
                guard let self else { return }
                switch newState {
                case .stopped:
                    self.state = .stopped
                case .starting:
                    self.state = .starting
                case .running:
                    self.state = .running(port: self.currentPort)
                case .failed(let err):
                    self.state = .failed(err)
                }
            }
        }
    }

    public func start(port: Int = 8080) async {
        guard !isRunning else { return }

        // First, check if a daemon is already running on the target port.
        // This handles the case where a brew-installed symseek daemon is
        // already serving (e.g. /opt/homebrew/bin/symseek serve).
        if await checkExistingDaemon(port: port) {
            appendLog("[engine] Detected running daemon on port \(port), adopting it")
            return
        }

        state = .starting
        appendLog("[engine] Starting symseek REST server on port \(port)…")

        guard let binaryURL = locateBinary() else {
            state = .failed("symseek binary not found in app bundle Resources")
            appendLog("[engine] ERROR: symseek binary not found")
            return
        }

        guard FileManager.default.isExecutableFile(atPath: binaryURL.path) else {
            state = .failed("symseek binary is not executable")
            appendLog("[engine] ERROR: binary not executable at \(binaryURL.path)")
            return
        }

        self.currentPort = port
        _ = supervisor.start(executable: binaryURL, arguments: ["serve", "--port", "\(port)"])
    }

    /// Stops the daemon without ever touching the supervisor's locking stop
    /// path from the main actor.  The supervisor call runs on the dedicated
    /// `stopQueue` background executor; the UI state afterwards is driven by
    /// `onStateChange` (→ `.stopped`/`.failed`) exactly as before.
    public func stop() async {
        let supervisor = self.supervisor
        await withCheckedContinuation { continuation in
            stopQueue.async {
                supervisor.stop()
                continuation.resume()
            }
        }
    }

    private func appendLog(_ message: String) {
        logs.append(message)
        if logs.count > maxLogs {
            logs.removeFirst(logs.count - maxLogs)
        }
    }

    /// Try to detect an already-running symseek daemon by making an
    /// authenticated status request to the given port.  Returns `true`
    /// and transitions the engine state to `.running` only when the
    /// daemon answers HTTP 200 with our bearer token; a 401 (bad/missing
    /// token) or any network error is treated as "no daemon", so
    /// `start()` falls through to launching one.
    private func checkExistingDaemon(port: Int) async -> Bool {
        guard await DaemonHealth.check(port: port, token: apiToken) else {
            return false
        }
        currentPort = port
        state = .running(port: port)
        return true
    }

    private func locateBinary() -> URL? {
        // Repo root (../symseek) as extra fallback keeps the pre-AppKit dev
        // workflow working when running without a bundled binary.
        let projectRoot = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent() // SymseekApp/
            .deletingLastPathComponent() // Sources/
            .deletingLastPathComponent() // client/
            .deletingLastPathComponent() // repo root
        let locator = BinaryLocator(extraDirectories: ["/opt/homebrew/bin", "/usr/local/bin", projectRoot.path])
        return locator.locate("symseek")?.url
    }
}
