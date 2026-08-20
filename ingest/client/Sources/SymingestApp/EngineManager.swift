import Foundation
import Observation
import SymairaDaemonKit

@Observable
@MainActor
public final class EngineManager {
    public enum State: Sendable, Equatable {
        case stopped
        case starting
        case running
        case failed(String)
    }

    public private(set) var state: State = .stopped
    public private(set) var logs: [String] = []

    public var isRunning: Bool {
        return state == .running
    }

    private let supervisor = DaemonSupervisor()
    private let maxLogs = 500

    public init() {
        setupSupervisor()
    }

    private func setupSupervisor() {
        supervisor.onLog = { [weak self] logLine in
            Task { @MainActor [weak self] in
                self?.appendLog(logLine.text)
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
                    self.state = .running
                case .failed(let err):
                    self.state = .failed(err)
                }
            }
        }
    }

    public func start(config: ConfigStore) async {
        guard !isRunning else { return }

        state = .starting
        appendLog("[watcher] Starting symingest watch…")

        guard !config.inboxPath.isEmpty else {
            state = .failed("No inbox directory configured")
            appendLog("[watcher] ERROR: Inbox path is empty. Please set it in Settings.")
            return
        }
        
        guard !config.vault.isEmpty else {
            state = .failed("No vault directory configured")
            appendLog("[watcher] ERROR: Vault path is empty. Please set it in Settings.")
            return
        }

        guard let binaryURL = CLIClient.shared.locateBinary(customPath: config.customBinaryPath) else {
            state = .failed("symingest binary not found")
            appendLog("[watcher] ERROR: symingest binary not found")
            return
        }

        guard FileManager.default.isExecutableFile(atPath: binaryURL.path) else {
            state = .failed("symingest binary is not executable")
            appendLog("[watcher] ERROR: binary not executable at \(binaryURL.path)")
            return
        }

        let arguments = [
            "watch",
            "--processing-dir", config.inboxPath + "/.processing",
            "--processed-dir", config.inboxPath + "/.processed",
            "--failed-dir", config.inboxPath + "/.failed",
            config.inboxPath
        ]

        // Set Environment
        var env = [String: String]()
        env["SYMINGEST_VAULT"] = config.vault
        env["SYMINGEST_ARCHIVE_PATH"] = config.archivePath
        env["SYMINGEST_DB_PATH"] = config.dbPath
        env["SYMINGEST_OCR_LANG"] = config.ocrLang
        
        _ = supervisor.start(executable: binaryURL, arguments: arguments, environment: env)
    }

    public func stop() {
        supervisor.stop()
    }

    private func appendLog(_ message: String) {
        logs.append(message)
        if logs.count > maxLogs {
            logs.removeFirst(logs.count - maxLogs)
        }
    }
}
