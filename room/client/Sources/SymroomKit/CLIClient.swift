import Foundation
import SymairaCLIRunner
import SymairaToolKit

public enum RoomCLIError: Error, LocalizedError, Sendable {
    case binaryNotFound
    case notARoom
    case executionFailed(code: Int, message: String)
    case invalidJSON(Error)

    public var errorDescription: String? {
        switch self {
        case .binaryNotFound:
            // danieljustus/tap/symroom is disabled since v0.10.0 — symroom
            // now ships inside the symdesk formula (#608).
            return "The symroom binary could not be found in PATH or Homebrew paths. Install it via 'brew install danieljustus/tap/symdesk' (symroom ships with Symaira Desktop since v0.10.0)."
        case .notARoom:
            return "The selected directory is not a room (no .symroom found). Run 'symroom init' there first."
        case .executionFailed(let code, let message):
            return "symroom failed with exit code \(code): \(message)"
        case .invalidJSON(let err):
            return "Failed to parse symroom output: \(err.localizedDescription)"
        }
    }
}

/// Narrow surface `RoomCLIClient` needs from `BinaryLocator.locate(_:allowUnverified:)`.
/// Abstracted so tests can inject a fake locator pointed at a temp directory
/// instead of depending on the real machine's PATH / Homebrew state.
/// `BinaryLocator` itself conforms via the extension below.
protocol SymroomLocating: Sendable {
    func locate(_ binaryName: String, allowUnverified: Bool) -> BinaryLocator.Located?
}

extension BinaryLocator: SymroomLocating {}

/// Thin bridge to the `symroom` CLI. The module renders `--json` output only;
/// it never reimplements room logic.
public final class RoomCLIClient: Sendable {
    private let decoder: JSONDecoder
    private let runner = CLIRunner(defaultTimeout: 60)

    /// Resolved once at init and reused by both `isInstalled` and every
    /// `run(...)` call, so the two can never disagree (#608).
    private let resolved: BinaryLocator.Located?

    /// Non-nil only when the strict search failed and a relaxed search
    /// (`allowUnverified: true`) found the binary instead — e.g. Homebrew's
    /// group-writable Apple Silicon prefix (`/opt/homebrew/bin`), which the
    /// strict `isDirectorySecure` check rejects by design. Names the
    /// accepted directory and the reason it failed strict verification so
    /// the UI can surface it rather than relaxing provenance silently.
    /// Mirrors `CoreBinaryDiscovery.Detection.provenanceNote` (#437), which
    /// only covered the `symdesk` core and never reached this module.
    public let provenanceNote: String?

    public convenience init() {
        // The managed runtime dir (~/.symaira/bin) is checked before the
        // Homebrew prefixes, matching DeskCore's locator (#459) — a
        // `symbrain setup`-managed install must be visible here too.
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let managedRuntimeDir = "\(home)/.symaira/bin"
        self.init(locator: BinaryLocator(
            extraDirectories: [managedRuntimeDir, "/opt/homebrew/bin", "/usr/local/bin"]
        ))
    }

    /// Test seam: accepts any `SymroomLocating`, not just the real
    /// `BinaryLocator`, so tests can inject deterministic strict/relaxed
    /// results instead of depending on the developer's machine.
    init(locator: SymroomLocating) {
        decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase

        // Strict search first; only fall back to the relaxed search when it
        // finds nothing. This is exactly the CoreBinaryDiscovery strategy
        // (#437) that the room module never got — without it, `isInstalled`
        // was always false on a stock Homebrew install (#608).
        if let strictHit = locator.locate("symroom", allowUnverified: false) {
            resolved = strictHit
            provenanceNote = nil
        } else if let relaxedHit = locator.locate("symroom", allowUnverified: true) {
            resolved = relaxedHit
            let directory = relaxedHit.url.deletingLastPathComponent().path
            provenanceNote = "Loaded symroom from \(directory). "
                + "That directory is group- or world-writable, so it did not pass the strict provenance check."
        } else {
            resolved = nil
            provenanceNote = nil
        }
    }

    public var isInstalled: Bool { resolved != nil }

    private func run(_ args: [String], in roomDir: String) async throws -> Data {
        guard let resolved else {
            throw RoomCLIError.binaryNotFound
        }
        do {
            // CLIRunner has no working-directory parameter, so the room is
            // passed via SYMROOM_ROOM_DIR (the CLI's documented override).
            return try await runner.runChecked(
                resolved.url,
                arguments: args,
                environment: ["SYMROOM_ROOM_DIR": roomDir]
            )
        } catch let CLIRunnerError.executionFailed(code, stderr) {
            throw RoomCLIError.executionFailed(code: Int(code), message: stderr)
        }
    }

    /// `symroom member list --json`
    public func listMembers(in roomDir: String) async throws -> [RoomMember] {
        let data = try await run(["member", "list", "--json"], in: roomDir)
        return try decode([RoomMember].self, from: data)
    }

    /// `symroom log --json` — NDJSON lines of journal events.
    public func journal(in roomDir: String, since: String? = nil, kind: String? = nil, author: String? = nil, limit: Int = 200) async throws -> [JournalEvent] {
        var args = ["log", "--json", "--limit", String(limit)]
        if let since { args += ["--since", since] }
        if let kind { args += ["--kind", kind] }
        if let author { args += ["--author", author] }
        let data = try await run(args, in: roomDir)
        return try Self.decodeJournalLines(data)
    }

    /// Splits NDJSON output and decodes every line strictly. A line that fails
    /// to decode (schema drift, corrupted output) throws instead of vanishing
    /// silently — a truncated journal must never render as a complete one.
    static func decodeJournalLines(_ data: Data) throws -> [JournalEvent] {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        let text = String(data: data, encoding: .utf8) ?? ""
        return try text.split(separator: "\n").map { line in
            try decoder.decode(JournalEvent.self, from: Data(line.utf8))
        }
    }

    /// `symroom run list --pending --json`
    public func pendingRuns(in roomDir: String) async throws -> [Run] {
        let data = try await run(["run", "list", "--pending", "--json"], in: roomDir)
        return try decode([Run].self, from: data)
    }

    /// `symroom run approve <id> [--scope ...] [--ttl ...]` — produces the same
    /// signed event as the CLI approval path.
    @discardableResult
    public func approve(runID: String, scope: String?, ttl: String?, in roomDir: String) async throws -> String {
        var args = ["run", "approve", runID]
        if let scope { args += ["--scope", scope] }
        if let ttl { args += ["--ttl", ttl] }
        let data = try await run(args, in: roomDir)
        return String(data: data, encoding: .utf8) ?? ""
    }

    /// `symroom run deny <id> --reason <reason>`
    @discardableResult
    public func deny(runID: String, reason: String, in roomDir: String) async throws -> String {
        let data = try await run(["run", "deny", runID, "--reason", reason], in: roomDir)
        return String(data: data, encoding: .utf8) ?? ""
    }

    private func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        do {
            return try decoder.decode(type, from: data)
        } catch {
            throw RoomCLIError.invalidJSON(error)
        }
    }
}
