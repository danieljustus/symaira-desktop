import Foundation
import SymairaToolKit
import SymairaCLIRunner

public enum DeskCoreError: Error {
    case coreNotFound
    case schemaMismatch(expected: Int, got: Int)
}

public struct Note: Codable, Equatable, Identifiable, Hashable {
    public var id: String { path }
    public let path: String
    public let title: String
    public let sha256: String
    public let modifiedAt: String
    public let indexedAt: String

    enum CodingKeys: String, CodingKey {
        case path, title, sha256
        case modifiedAt = "modified_at"
        case indexedAt = "indexed_at"
    }
}

public struct DeskStatus: Codable {
    public let version: String
    public let schemaVersion: Int
    
    enum CodingKeys: String, CodingKey {
        case version
        case schemaVersion = "schema_version"
    }
}

public struct SearchResult: Codable, Equatable, Identifiable {
    public var id: String { path }
    public let path: String
    public let title: String
    public let snippet: String
    public let score: Double?
}

@MainActor
public final class DeskCore: ObservableObject {
    public static let shared = DeskCore()
    
    private let locator = BinaryLocator(bundle: Bundle.main)
    private var detector: ToolDetector { ToolDetector(locator: locator) }
    
    @Published public private(set) var tool: DetectedTool?
    @Published public private(set) var isReady = false
    @Published public private(set) var errorMessage: String?
    
    private init() {}
    
    public func initialize() async {
        guard let deskTool = SymairaToolRegistry.tool(id: "symdesk") else {
            self.errorMessage = "symdesk not found in registry"
            return
        }
        
        guard let detected = await detector.detect(deskTool) else {
            self.errorMessage = "symdesk binary not found. Please install via Homebrew."
            return
        }
        
        do {
            try detector.requireSchemaVersion(1, of: detected)
            self.tool = detected
            self.isReady = true
        } catch {
            self.errorMessage = "symdesk schema mismatch: \(error)"
        }
    }
    
    public func listFiles() async throws -> [Note] {
        guard let tool else { throw DeskCoreError.coreNotFound }
        
        let runner = CLIRunner()
        return try await runner.runDecoding(
            [Note].self,
            executable: tool.location.url,
            arguments: ["ls", "--json"]
        )
    }
    
    public func getDoctor() async throws -> String {
        guard let tool else { throw DeskCoreError.coreNotFound }
        
        let runner = CLIRunner()
        let out = try await runner.runChecked(tool.location.url, arguments: ["doctor"])
        return String(decoding: out, as: UTF8.self)
    }
    
    public func search(query: String) async throws -> [SearchResult] {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            [SearchResult].self,
            executable: tool.location.url,
            arguments: ["search", query, "--json"]
        )
    }
    
    public func backlinks(for path: String) async throws -> [String] {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            [String].self,
            executable: tool.location.url,
            arguments: ["backlinks", path, "--json"]
        )
    }
    
    public func noteNew(title: String) async throws -> String {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        // symdesk note new --title <title> --json
        // Returns e.g. {"path": "..."}
        struct NoteNewResult: Codable {
            let path: String
        }
        let res = try await runner.runDecoding(
            NoteNewResult.self,
            executable: tool.location.url,
            arguments: ["note", "new", "--title", title, "--json"]
        )
        return res.path
    }
}
