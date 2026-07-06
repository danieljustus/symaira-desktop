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

public struct DbFilter: Codable, Equatable {
    public let key: String
    public let operatorString: String
    public let value: String
    
    enum CodingKeys: String, CodingKey {
        case key
        case operatorString = "operator"
        case value
    }
}

public struct DbSort: Codable, Equatable {
    public let key: String
    public let ascending: Bool
}

public struct DbView: Codable, Equatable, Identifiable {
    public let id: String
    public let name: String
    public let filters: [DbFilter]
    public let sorts: [DbSort]
    public let columns: [String]
}

public struct GraphNode: Codable, Equatable, Identifiable {
    public let id: String
    public let label: String
}

public struct GraphEdge: Codable, Equatable, Identifiable {
    public var id: String { "\(source)->\(target)" }
    public let source: String
    public let target: String
}

public struct GraphData: Codable, Equatable {
    public let nodes: [GraphNode]
    public let edges: [GraphEdge]
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
    
    public func getGraph() async throws -> GraphData {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            GraphData.self,
            executable: tool.location.url,
            arguments: ["graph", "--json"]
        )
    }
    
    public func viewsList() async throws -> [DbView] {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            [DbView].self,
            executable: tool.location.url,
            arguments: ["views", "list", "--json"]
        )
    }
    
    public func viewsGet(id: String) async throws -> DbView {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            DbView.self,
            executable: tool.location.url,
            arguments: ["views", "get", id, "--json"]
        )
    }
    
    // We expect an array of JSON objects. In Swift we can use [String: AnyCodable] or just a loose representation.
    // For simplicity, let's use [String: String] since sqlite snippet / properties are strings, or a generic dictionary.
    // However, Swift's Codable doesn't do [String: Any] easily without a custom wrapper.
    // Let's use `Data` and let the UI decode it or use a simple struct.
    public func viewsExec(id: String) async throws -> Data {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        // We runChecked and just return the raw JSON data so the UI can parse it dynamically
        return try await runner.runChecked(
            tool.location.url,
            arguments: ["views", "exec", id, "--json"]
        )
    }
}
