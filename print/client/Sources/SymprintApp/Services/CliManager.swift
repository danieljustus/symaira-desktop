import Foundation
import Observation
import SymairaCLIRunner
import SymairaToolKit

@Observable
@MainActor
class CliManager {
    static let shared = CliManager()

    private(set) var isExecuting = false
    private(set) var logs: [String] = []

    // Large documents can render for minutes; keep the timeout generous.
    private let runner = CLIRunner(defaultTimeout: 300)
    private let locator: BinaryLocator = {
        // Repo root (../symprint) as last resort keeps the pre-AppKit dev
        // workflow working when running from Xcode without a bundled binary.
        let projectRoot = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent() // Services/
            .deletingLastPathComponent() // SymprintApp/
            .deletingLastPathComponent() // Sources/
            .deletingLastPathComponent() // client/
            .deletingLastPathComponent() // repo root
        return BinaryLocator(extraDirectories: ["/opt/homebrew/bin", "/usr/local/bin", projectRoot.path])
    }()

    private func appendLog(_ text: String) {
        logs.append(text)
        if logs.count > 1000 {
            logs.removeFirst(logs.count - 1000)
        }
    }

    func clearLogs() {
        logs.removeAll()
    }

    private func locateBinary() -> URL? {
        locator.locate("symprint")?.url
    }

    func executeCommand(arguments: [String]) async throws -> (stdout: String, stderr: String, exitCode: Int32) {
        guard let binaryURL = locateBinary() else {
            throw NSError(domain: "CliManager", code: 404, userInfo: [NSLocalizedDescriptionKey: "symprint binary not found. Install it via 'brew install danieljustus/tap/symprint' or build it first ('make build')."])
        }

        let result = try await runner.run(binaryURL, arguments: arguments)
        return (
            String(data: result.stdout, encoding: .utf8) ?? "",
            String(data: result.stderr, encoding: .utf8) ?? "",
            result.exitCode
        )
    }
    
    // MARK: - API commands
    
    func getDoctor() async -> DoctorResult? {
        do {
            let res = try await executeCommand(arguments: ["doctor", "--json"])
            if res.exitCode == 0, let data = res.stdout.data(using: .utf8) {
                return try JSONDecoder().decode(DoctorResult.self, from: data)
            }
        } catch {
            print("Error executing doctor command: \(error)")
        }
        return nil
    }
    
    func getProfiles() async -> [CliProfile] {
        do {
            let res = try await executeCommand(arguments: ["profiles", "--json"])
            if res.exitCode == 0, let data = res.stdout.data(using: .utf8) {
                return try JSONDecoder().decode([CliProfile].self, from: data)
            }
        } catch {
            print("Error executing profiles command: \(error)")
        }
        return []
    }
    
    /// Runs the engine's frontmatter contract check and returns the profile
    /// named in the document's frontmatter (empty when the document declares
    /// none). Used to preselect the render profile for a picked document.
    func validate(inputPath: String) async -> ValidateResult? {
        do {
            let res = try await executeCommand(arguments: ["validate", inputPath, "--json"])
            if res.exitCode == 0, let data = res.stdout.data(using: .utf8) {
                return try JSONDecoder().decode(ValidateResult.self, from: data)
            }
        } catch {
            print("Error executing validate command: \(error)")
        }
        return nil
    }
    
    func getConfigPath() async -> String {
        do {
            let res = try await executeCommand(arguments: ["config", "path"])
            if res.exitCode == 0 {
                return res.stdout.trimmingCharacters(in: .whitespacesAndNewlines)
            }
        } catch {
            print("Error executing config path command: \(error)")
        }
        return FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".config/symprint/config.toml").path
    }
    
    func initializeConfig() async -> Bool {
        do {
            let res = try await executeCommand(arguments: ["config", "init"])
            return res.exitCode == 0
        } catch {
            print("Error executing config init command: \(error)")
            return false
        }
    }
    
    func render(
        inputPath: String,
        outputPath: String,
        profile: String?,
        standard: String?,
        reproducible: Bool?,
        fontPath: String?
    ) async throws -> RenderResult {
        isExecuting = true
        appendLog("[symprint] Rendering \(inputPath)…")
        
        var args = ["render", inputPath, "-o", outputPath, "--json"]
        if let profile = profile, !profile.isEmpty {
            args += ["--profile", profile]
        }
        if let standard = standard, !standard.isEmpty {
            args += ["--pdf-standard", standard]
        }
        if let fontPath = fontPath, !fontPath.isEmpty {
            args += ["--font-path", fontPath]
        }
        if let reproducible = reproducible {
            if reproducible {
                args += ["--reproducible"]
            }
        }
        
        do {
            let res = try await executeCommand(arguments: args)
            isExecuting = false
            
            // Clean up outputs and log them
            let stderrStr = res.stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            let stdoutStr = res.stdout.trimmingCharacters(in: .whitespacesAndNewlines)
            
            if !stderrStr.isEmpty {
                appendLog("[stderr] \(stderrStr)")
            }
            if !stdoutStr.isEmpty && res.exitCode != 0 {
                appendLog("[stdout] \(stdoutStr)")
            }
            
            if res.exitCode == 0, let data = res.stdout.data(using: .utf8) {
                let renderRes = try JSONDecoder().decode(RenderResult.self, from: data)
                appendLog("[symprint] Render successful. Output: \(renderRes.outputPath) (\(String(format: "%.1f", Double(renderRes.bytes)/1024.0)) kB) in \(renderRes.durationMs)ms")
                return renderRes
            } else {
                let cleanErr = stderrStr.isEmpty ? "Render command failed with exit code \(res.exitCode)" : stderrStr
                throw NSError(domain: "CliManager", code: Int(res.exitCode), userInfo: [NSLocalizedDescriptionKey: cleanErr])
            }
        } catch {
            isExecuting = false
            appendLog("[error] Render failed: \(error.localizedDescription)")
            throw error
        }
    }
}
