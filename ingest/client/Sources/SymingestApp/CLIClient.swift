import Foundation
import SymairaCLIRunner
import SymairaToolKit

public struct IngestJob: Codable, Identifiable, Sendable {
    public let id: Int64
    public let documentId: Int64
    public let kind: String
    public let status: String
    public let attempts: Int
    public let lastError: String?
    public let createdAt: String
    public let updatedAt: String
    public let sourcePath: String

    enum CodingKeys: String, CodingKey {
        case id
        case documentId = "document_id"
        case kind
        case status
        case attempts
        case lastError = "last_error"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case sourcePath = "source_path"
    }
}

public struct SwiftRule: Codable, Identifiable, Sendable {
    public let id: Int64
    public let pattern: String
    public let kind: String
    public let value: String
    public let createdAt: String

    enum CodingKeys: String, CodingKey {
        case id
        case pattern
        case kind
        case value
        case createdAt = "created_at"
    }
}

private protocol RulesJSONEnvelope: Decodable {
    var schemaVersion: Int { get }
}

public struct RulesListResponse: Codable, Sendable, RulesJSONEnvelope {
    public let schemaVersion: Int
    public let rules: [SwiftRule]

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case rules
    }
}

public struct RuleResponse: Codable, Sendable, RulesJSONEnvelope {
    public let schemaVersion: Int
    public let rule: SwiftRule

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case rule
    }
}

public struct SwiftRuleMatch: Codable, Sendable {
    public let id: Int64
    public let pattern: String
    public let kind: String
    public let value: String
}

public struct RuleTestResponse: Codable, Sendable, RulesJSONEnvelope {
    public let schemaVersion: Int
    public let matches: [SwiftRuleMatch]

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case matches
    }
}

public struct RuleDeleteResponse: Codable, Sendable, RulesJSONEnvelope {
    public let schemaVersion: Int
    public let id: Int64
    public let deleted: Bool

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case id
        case deleted
    }
}

private enum RulesJSONError: LocalizedError {
    case unsupportedSchema(Int)
    case invalidResponse(String)

    var errorDescription: String? {
        switch self {
        case .unsupportedSchema(let version):
            return "Unsupported symingest rules JSON schema version \(version). Update symingest to continue."
        case .invalidResponse(let message):
            return "Invalid symingest rules JSON response: \(message)"
        }
    }
}

public struct ReviewFindingDTO: Codable, Identifiable, Sendable {
    public var id: String { "\(kind)-\(documentID ?? 0)-\(message)" }
    public let kind: String
    public let documentID: Int?
    public let message: String

    enum CodingKeys: String, CodingKey {
        case kind
        case documentID = "id"
        case message
    }
}

public struct ReviewDocumentDTO: Codable, Identifiable, Sendable {
    public let id: Int
    public let status: String
    public let reason: String?
    public let mime: String?
    public let expectedExtension: String?
    public let vaultPath: String?
    public let archivePath: String?
    public let error: String?
    public let warnings: [String]?
    public let findings: [String]?

    enum CodingKeys: String, CodingKey {
        case id, status, reason, mime, error, warnings, findings
        case expectedExtension = "expected_extension"
        case vaultPath = "vault_path"
        case archivePath = "archive_path"
    }
}

public struct ReviewReportDTO: Codable, Sendable {
    public let schemaVersion: Int
    public let sourceKind: String
    public let runID: String?
    public let total: Int
    public let documents: [ReviewDocumentDTO]
    public let findings: [ReviewFindingDTO]?
    public let warnings: [String]?

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case sourceKind = "source_kind"
        case runID = "run_id"
        case total, documents, findings, warnings
    }
}

public enum StreamKind: Sendable {
    case stdout
    case stderr
}

public struct DependencyReport: Sendable {
    public let symingestPath: String?
    public let tesseractPath: String?
    public let pdftoppmPath: String?
    public let sipsPath: String?
    public let textutilPath: String?
    public let pandocPath: String?
    public let libreOfficePath: String?
    public let sofficePath: String?

    public var isComplete: Bool {
        return symingestPath != nil && tesseractPath != nil && pdftoppmPath != nil
    }
}

public struct CLIConfigSnapshot: Sendable {
    public let vault: String
    public let ocrLang: String
    public let dbPath: String
    public let archivePath: String
    public let inboxPath: String
    public let customBinaryPath: String
    public let symseekEnabled: Bool
    public let symseekBinary: String

    @MainActor
    public init(config: ConfigStore) {
        self.vault = config.vault
        self.ocrLang = config.ocrLang
        self.dbPath = config.dbPath
        self.archivePath = config.archivePath
        self.inboxPath = config.inboxPath
        self.customBinaryPath = config.customBinaryPath
        self.symseekEnabled = config.symseekEnabled
        self.symseekBinary = config.symseekBinary
    }
}

public final class CLIClient: Sendable {
    public static let shared = CLIClient()

    private static let rulesJSONSchemaVersion = 1

    // OCR runs over large PDFs can take a while; keep the timeout generous.
    private let runner = CLIRunner(defaultTimeout: 600)

    private init() {}

    private func decodeRulesResponse<T: Decodable & RulesJSONEnvelope>(_ type: T.Type, output: String) throws -> T {
        guard let data = output.data(using: .utf8) else {
            throw RulesJSONError.invalidResponse("output is not valid UTF-8")
        }
        do {
            let response = try JSONDecoder().decode(type, from: data)
            guard response.schemaVersion == Self.rulesJSONSchemaVersion else {
                throw RulesJSONError.unsupportedSchema(response.schemaVersion)
            }
            return response
        } catch let error as RulesJSONError {
            throw error
        } catch {
            throw RulesJSONError.invalidResponse(error.localizedDescription)
        }
    }

    private func ruleMessage(_ action: String, rule: SwiftRule) -> String {
        "\(action) classification rule \(rule.id): pattern=\"\(rule.pattern)\", kind=\"\(rule.kind)\", value=\"\(rule.value)\""
    }

    private func ruleMatchesMessage(_ matches: [SwiftRuleMatch]) -> String {
        guard !matches.isEmpty else { return "No matching classification rules." }
        return matches.map { match in
            "match rule \(match.id): pattern=\"\(match.pattern)\" kind=\"\(match.kind)\" value=\"\(match.value)\""
        }.joined(separator: "\n")
    }

    public func locateBinary(customPath: String) -> URL? {
        let override = customPath.isEmpty ? nil : URL(fileURLWithPath: customPath)
        return BinaryLocator(userOverride: override).locate("symingest")?.url
    }

    /// Shared discovery (bundle → exe dir → PATH → Homebrew prefixes) —
    /// also used for the external helpers (tesseract, pdftoppm, sips).
    /// Replaces the former `/usr/bin/which` subprocess.
    private func searchPathFor(_ executable: String) -> String? {
        BinaryLocator().locate(executable)?.url.path
    }

    public func checkDependencies(customPath: String) async -> DependencyReport {
        let sym = locateBinary(customPath: customPath)?.path
        let tess = searchPathFor("tesseract")
        let pdf = searchPathFor("pdftoppm")
        let sips = searchPathFor("sips")
        let textutil = searchPathFor("textutil")
        let pandoc = searchPathFor("pandoc")
        let libreOffice = searchPathFor("libreoffice")
        let soffice = searchPathFor("soffice")
        return DependencyReport(
            symingestPath: sym,
            tesseractPath: tess,
            pdftoppmPath: pdf,
            sipsPath: sips,
            textutilPath: textutil,
            pandocPath: pandoc,
            libreOfficePath: libreOffice,
            sofficePath: soffice
        )
    }

    private func applyConfigEnvironment(_ config: CLIConfigSnapshot, to env: inout [String: String]) {
        if !config.vault.isEmpty { env["SYMINGEST_VAULT"] = config.vault }
        if !config.archivePath.isEmpty { env["SYMINGEST_ARCHIVE_PATH"] = config.archivePath }
        if !config.dbPath.isEmpty { env["SYMINGEST_DB_PATH"] = config.dbPath }
        if !config.ocrLang.isEmpty { env["SYMINGEST_OCR_LANG"] = config.ocrLang }
        if !config.inboxPath.isEmpty { env["SYMINGEST_INBOX"] = config.inboxPath }
        env["SYMINGEST_SYMSEEK_ENABLED"] = config.symseekEnabled ? "true" : "false"
        if !config.symseekBinary.isEmpty { env["SYMINGEST_SYMSEEK_BINARY"] = config.symseekBinary }
    }

    public func runIngestCommand(args: [String], config: ConfigStore, environment: [String: String] = [:]) async throws -> (stdout: String, stderr: String) {
        let snapshot = await CLIConfigSnapshot(config: config)
        guard let binary = locateBinary(customPath: snapshot.customBinaryPath) else {
            throw NSError(domain: "symingest", code: 404, userInfo: [NSLocalizedDescriptionKey: "symingest binary not found"])
        }

        // Config env vars (SYMINGEST_*) merged over the PATH-augmented
        // environment by CLIRunner.
        var env: [String: String] = [:]
        applyConfigEnvironment(snapshot, to: &env)
        environment.forEach { key, value in env[key] = value }

        // Pre-existing contract: non-zero exits are NOT thrown here —
        // callers inspect stdout/stderr themselves.
        let result = try await runner.run(binary, arguments: args, environment: env)
        return (
            String(data: result.stdout, encoding: .utf8) ?? "",
            String(data: result.stderr, encoding: .utf8) ?? ""
        )
    }

    // MARK: - Business Operations

    public func listJobs(config: ConfigStore) async throws -> [IngestJob] {
        let (out, _) = try await runIngestCommand(args: ["jobs", "--json"], config: config)
        let decoder = JSONDecoder()
        return try decoder.decode([IngestJob].self, from: out.data(using: .utf8) ?? Data())
    }

    public func retryJob(id: Int64, config: ConfigStore) async -> (success: Bool, message: String) {
        do {
            let (out, err) = try await runIngestCommand(args: ["retry", "\(id)"], config: config)
            if out.contains("pending") {
                return (true, out.trimmingCharacters(in: .whitespacesAndNewlines))
            } else {
                return (false, err.isEmpty ? out : err)
            }
        } catch {
            return (false, error.localizedDescription)
        }
    }

    public func listRules(config: ConfigStore) async throws -> [SwiftRule] {
        let (out, _) = try await runIngestCommand(args: ["rules", "--json", "list"], config: config)
        return try decodeRulesResponse(RulesListResponse.self, output: out).rules
    }

    public func addRule(pattern: String, kind: String, value: String, config: ConfigStore) async -> (success: Bool, message: String) {
        do {
            let (out, _) = try await runIngestCommand(args: ["rules", "--json", "add", pattern, kind, value], config: config)
            let response = try decodeRulesResponse(RuleResponse.self, output: out)
            return (true, ruleMessage("Added", rule: response.rule))
        } catch {
            return (false, error.localizedDescription)
        }
    }

    public func deleteRule(id: Int64, config: ConfigStore) async -> (success: Bool, message: String) {
        do {
            let (out, _) = try await runIngestCommand(args: ["rules", "--json", "delete", "\(id)"], config: config)
            let response = try decodeRulesResponse(RuleDeleteResponse.self, output: out)
            guard response.deleted else {
                return (false, "symingest did not delete classification rule \(response.id)")
            }
            return (true, "Deleted classification rule \(response.id).")
        } catch {
            return (false, error.localizedDescription)
        }
    }

    public func updateRule(id: Int64, pattern: String, kind: String, value: String, config: ConfigStore) async -> (success: Bool, message: String) {
        do {
            let (out, _) = try await runIngestCommand(args: ["rules", "--json", "update", "\(id)", pattern, kind, value], config: config)
            let response = try decodeRulesResponse(RuleResponse.self, output: out)
            return (true, ruleMessage("Updated", rule: response.rule))
        } catch {
            return (false, error.localizedDescription)
        }
    }

    public func testRules(text: String, config: ConfigStore) async -> (success: Bool, message: String) {
        do {
            let (out, _) = try await runIngestCommand(args: ["rules", "--json", "test", text], config: config)
            let response = try decodeRulesResponse(RuleTestResponse.self, output: out)
            return (true, ruleMatchesMessage(response.matches))
        } catch {
            return (false, error.localizedDescription)
        }
    }

    public func ingestFile(filePath: String, config: ConfigStore) async -> (success: Bool, message: String) {
        do {
            let (out, err) = try await runIngestCommand(args: ["ingest", filePath], config: config)
            if out.contains("ingested") || out.contains("already ingested") {
                return (true, out.trimmingCharacters(in: .whitespacesAndNewlines))
            } else {
                return (false, err.isEmpty ? out : err)
            }
        } catch {
            return (false, error.localizedDescription)
        }
    }

    public func buildReviewReport(reportPath: String, filters: [String], config: ConfigStore) async throws -> ReviewReportDTO {
        let args = ["review-report", "--json"] + filters + [reportPath]
        let (out, err) = try await runIngestCommand(args: args, config: config)
        let data = out.data(using: .utf8) ?? Data()
        do {
            return try JSONDecoder().decode(ReviewReportDTO.self, from: data)
        } catch {
            throw NSError(domain: "symingest", code: 422, userInfo: [NSLocalizedDescriptionKey: err.isEmpty ? "Failed to parse review report JSON" : err])
        }
    }

    public func writeReviewHTML(reportPath: String, htmlPath: String, filters: [String], config: ConfigStore) async throws -> String {
        let args = ["review-report", "--html", htmlPath] + filters + [reportPath]
        let (out, err) = try await runIngestCommand(args: args, config: config)
        return (err.isEmpty ? out : err).trimmingCharacters(in: .whitespacesAndNewlines)
    }

    public func applyCorrections(vault: String, correctionsPath: String, dryRun: Bool, requireCount: Int?, max: Int?, backupDir: String, config: ConfigStore) async throws -> String {
        var args = ["apply-corrections", "--vault", vault]
        if dryRun { args.append("--dry-run") }
        if let requireCount { args += ["--require-count", "\(requireCount)"] }
        if let max { args += ["--max", "\(max)"] }
        if !backupDir.isEmpty { args += ["--backup-dir", backupDir] }
        args.append(correctionsPath)
        let (out, err) = try await runIngestCommand(args: args, config: config)
        return (err.isEmpty ? out : err).trimmingCharacters(in: .whitespacesAndNewlines)
    }

    public func service(command: String, dryRun: Bool = false, json: Bool = false, config: ConfigStore) async -> (success: Bool, message: String) {
        do {
            let snapshot = await CLIConfigSnapshot(config: config)
            var args = ["service"]
            if dryRun { args.append("--dry-run") }
            if json { args.append("--json") }
            if !snapshot.vault.isEmpty { args += ["--vault", snapshot.vault] }
            if !snapshot.archivePath.isEmpty { args += ["--archive", snapshot.archivePath] }
            if !snapshot.dbPath.isEmpty { args += ["--db", snapshot.dbPath] }
            if !snapshot.ocrLang.isEmpty { args += ["--ocr-lang", snapshot.ocrLang] }
            if !snapshot.inboxPath.isEmpty { args += ["--inbox", snapshot.inboxPath] }
            args.append(command)
            let (out, err) = try await runIngestCommand(args: args, config: config)
            let message = (err.isEmpty ? out : err).trimmingCharacters(in: .whitespacesAndNewlines)
            return (err.isEmpty, message)
        } catch {
            return (false, error.localizedDescription)
        }
    }

    public func serviceLogs(config: ConfigStore) async -> (success: Bool, message: String) {
        do {
            let snapshot = await CLIConfigSnapshot(config: config)
            var args = ["service", "--lines", "120"]
            if !snapshot.vault.isEmpty { args += ["--vault", snapshot.vault] }
            if !snapshot.archivePath.isEmpty { args += ["--archive", snapshot.archivePath] }
            if !snapshot.dbPath.isEmpty { args += ["--db", snapshot.dbPath] }
            if !snapshot.ocrLang.isEmpty { args += ["--ocr-lang", snapshot.ocrLang] }
            if !snapshot.inboxPath.isEmpty { args += ["--inbox", snapshot.inboxPath] }
            args.append("logs")
            let (out, err) = try await runIngestCommand(args: args, config: config)
            let message = (err.isEmpty ? out : err).trimmingCharacters(in: .whitespacesAndNewlines)
            return (err.isEmpty, message)
        } catch {
            return (false, error.localizedDescription)
        }
    }

    public func indexVault(config: ConfigStore) async -> (success: Bool, message: String) {
        do {
            let snapshot = await CLIConfigSnapshot(config: config)
            var args = ["search", "--json"]
            if !snapshot.vault.isEmpty { args += ["--vault", snapshot.vault] }
            if !snapshot.symseekBinary.isEmpty { args += ["--symseek-binary", snapshot.symseekBinary] }
            args.append("index")
            let (out, err) = try await runIngestCommand(args: args, config: config)
            let message = (err.isEmpty ? out : err).trimmingCharacters(in: .whitespacesAndNewlines)
            return (err.isEmpty && out.contains("\"ok\": true"), message)
        } catch {
            return (false, error.localizedDescription)
        }
    }

    public func runIngestCommandStreaming(
        args: [String],
        config: ConfigStore,
        environment: [String: String] = [:],
        onOutput: @escaping @Sendable (String) -> Void
    ) async throws -> Int32 {
        try await runIngestCommandStreaming(args: args, config: config, environment: environment) { text, _ in
            onOutput(text)
        }
    }

    public func runIngestCommandStreaming(
        args: [String],
        config: ConfigStore,
        environment: [String: String] = [:],
        onOutput: @escaping @Sendable (String, StreamKind) -> Void
    ) async throws -> Int32 {
        let snapshot = await CLIConfigSnapshot(config: config)
        guard let binary = locateBinary(customPath: snapshot.customBinaryPath) else {
            throw NSError(domain: "symingest", code: 404, userInfo: [NSLocalizedDescriptionKey: "symingest binary not found"])
        }

        let process = Process()
        process.executableURL = binary
        process.arguments = args

        // Environment variables matching CLI/XDG
        var env = ProcessInfo.processInfo.environment
        applyConfigEnvironment(snapshot, to: &env)
        environment.forEach { key, value in env[key] = value }

        // Add homebrew paths to process PATH if missing
        if let path = env["PATH"] {
            env["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:\(path)"
        } else {
            env["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
        }
        process.environment = env

        let outPipe = Pipe()
        let errPipe = Pipe()
        process.standardOutput = outPipe
        process.standardError = errPipe

        let outHandle = outPipe.fileHandleForReading
        let errHandle = errPipe.fileHandleForReading

        outHandle.readabilityHandler = { handle in
            let data = handle.availableData
            guard !data.isEmpty, let text = String(data: data, encoding: .utf8) else { return }
            onOutput(text, .stdout)
        }

        errHandle.readabilityHandler = { handle in
            let data = handle.availableData
            guard !data.isEmpty, let text = String(data: data, encoding: .utf8) else { return }
            onOutput(text, .stderr)
        }

        try process.run()
        process.waitUntilExit()

        outHandle.readabilityHandler = nil
        errHandle.readabilityHandler = nil

        return process.terminationStatus
    }
}
