import Foundation
import SymairaToolKit
import SymairaCLIRunner

public enum DeskCoreError: Error {
    case coreNotFound
    case schemaMismatch(expected: Int, got: Int)
}

public struct Note: Codable, Equatable, Identifiable, Hashable, Sendable {
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

public struct DeskStatus: Codable, Sendable {
    public let version: String
    public let schemaVersion: Int

    enum CodingKeys: String, CodingKey {
        case version
        case schemaVersion = "schema_version"
    }
}

public struct SearchResult: Codable, Equatable, Identifiable, Sendable {
    public var id: String { path }
    public let path: String
    public let title: String
    public let snippet: String
    public let score: Double?
}

public struct DbFilter: Codable, Equatable, Sendable {
    public let key: String
    public let operatorString: String
    public let value: String

    enum CodingKeys: String, CodingKey {
        case key
        case operatorString = "operator"
        case value
    }
}

public struct DbSort: Codable, Equatable, Sendable {
    public let key: String
    public let ascending: Bool
}

public struct DbView: Codable, Equatable, Identifiable, Sendable {
    public let id: String
    public let name: String
    public let filters: [DbFilter]
    public let sorts: [DbSort]
    public let columns: [String]
}

public struct DocumentItem: Codable, Equatable, Identifiable, Sendable {
    public var id: String { path }
    public let path: String
    public let title: String
    public let documentDate: String
    public let person: String
    public let status: String
    public let dueDate: String
    public let confidence: Int
    public let correspondent: String
    public let documentType: String

    enum CodingKeys: String, CodingKey {
        case path, title, person, status, confidence, correspondent
        case documentDate = "document_date"
        case dueDate = "due_date"
        case documentType = "document_type"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        title = try c.decode(String.self, forKey: .title)
        documentDate = (try? c.decode(String.self, forKey: .documentDate)) ?? ""
        person = (try? c.decode(String.self, forKey: .person)) ?? ""
        status = (try? c.decode(String.self, forKey: .status)) ?? ""
        dueDate = (try? c.decode(String.self, forKey: .dueDate)) ?? ""
        confidence = (try? c.decode(Int.self, forKey: .confidence)) ?? 0
        correspondent = (try? c.decode(String.self, forKey: .correspondent)) ?? ""
        documentType = (try? c.decode(String.self, forKey: .documentType)) ?? ""
    }
}

public enum DocumentStatus: String, CaseIterable, Identifiable, Sendable {
    case open
    case paid
    case submitted
    case done
    case needsReview = "needs_review"
    case waitingForReply = "waiting_for_reply"

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .open: return "Open"
        case .paid: return "Paid"
        case .submitted: return "Submitted"
        case .done: return "Done"
        case .needsReview: return "Needs Review"
        case .waitingForReply: return "Waiting for Reply"
        }
    }

    public var systemImage: String {
        switch self {
        case .open: return "circle"
        case .paid: return "checkmark.circle"
        case .submitted: return "paperplane"
        case .done: return "checkmark.circle.fill"
        case .needsReview: return "exclamationmark.triangle"
        case .waitingForReply: return "clock"
        }
    }
}

public struct DocFilterPreset: Identifiable, Sendable {
    public let id: String
    public let label: String
    public let status: DocumentStatus?

    public init(id: String, label: String, status: DocumentStatus?) {
        self.id = id
        self.label = label
        self.status = status
    }

    public static let defaults: [DocFilterPreset] = [
        .init(id: "all", label: "All Documents", status: nil),
        .init(id: "open", label: "Open", status: .open),
        .init(id: "needs_review", label: "Needs Review", status: .needsReview),
        .init(id: "waiting_for_reply", label: "Waiting for Reply", status: .waitingForReply),
        .init(id: "submitted", label: "Submitted", status: .submitted),
        .init(id: "done", label: "Done", status: .done),
        .init(id: "paid", label: "Paid", status: .paid),
    ]
}

public struct GraphNode: Codable, Equatable, Identifiable, Sendable {
    public let id: String
    public let label: String
}

public struct GraphEdge: Codable, Equatable, Identifiable, Sendable {
    public var id: String { "\(source)->\(target)" }
    public let source: String
    public let target: String
}

public struct GraphData: Codable, Equatable, Sendable {
    public let nodes: [GraphNode]
    public let edges: [GraphEdge]

    public init(nodes: [GraphNode], edges: [GraphEdge]) {
        self.nodes = nodes
        self.edges = edges
    }
}

@MainActor
public final class DeskCore: ObservableObject {
    public static let shared = DeskCore()

    private let locator = BinaryLocator(bundle: Bundle.main)
    private var detector: ToolDetector { ToolDetector(locator: locator) }

    @Published public private(set) var tool: DetectedTool?
    @Published public private(set) var isReady = false
    @Published public private(set) var errorMessage: String?

    @Published public var vaultPath: String?

    public var isDemoMode: Bool { VaultConfig.isDemoMode }

    private init() {}

    /// Appends `--vault <path>` when a vault is configured, empty otherwise.
    private var vaultArgs: [String] {
        guard let path = vaultPath, !path.isEmpty else { return [] }
        return ["--vault", path]
    }

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
            arguments: ["ls", "--json"] + vaultArgs
        )
    }

    public func getDoctor() async throws -> String {
        guard let tool else { throw DeskCoreError.coreNotFound }

        let runner = CLIRunner()
        let out = try await runner.runChecked(tool.location.url,             arguments: ["doctor"] + vaultArgs)
        return String(decoding: out, as: UTF8.self)
    }

    public func search(query: String) async throws -> [SearchResult] {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            [SearchResult].self,
            executable: tool.location.url,
            arguments: ["search", query, "--json"] + vaultArgs
        )
    }

    public func backlinks(for path: String) async throws -> [String] {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            [String].self,
            executable: tool.location.url,
            arguments: ["backlinks", path, "--json"] + vaultArgs
        )
    }

    public func noteNew(title: String) async throws -> String {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        // symdesk note new --title <title> --json
        // Returns e.g. {"path": "..."}
        struct NoteNewResult: Codable, Sendable {
            let path: String
        }
        let res = try await runner.runDecoding(
            NoteNewResult.self,
            executable: tool.location.url,
            arguments: ["note", "new", "--title", title, "--json"] + vaultArgs
        )
        return res.path
    }

    public func noteEditProperty(path: String, key: String, value: String) async throws {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        _ = try await runner.runChecked(
            tool.location.url,
            arguments: ["props", "edit", path, key, value] + vaultArgs
        )
    }

    public func getGraph() async throws -> GraphData {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            GraphData.self,
            executable: tool.location.url,
            arguments: ["graph", "--json"] + vaultArgs
        )
    }

    public func viewsList() async throws -> [DbView] {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            [DbView].self,
            executable: tool.location.url,
            arguments: ["views", "list", "--json"] + vaultArgs
        )
    }

    public func viewsGet(id: String) async throws -> DbView {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            DbView.self,
            executable: tool.location.url,
            arguments: ["views", "get", id, "--json"] + vaultArgs
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
            arguments: ["views", "exec", id, "--json"] + vaultArgs
        )
    }
    public func ingest(fileURL: URL) async throws -> String {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        struct IngestRes: Codable, Sendable {
            let path: String
        }
        let res = try await runner.runDecoding(
            IngestRes.self,
            executable: tool.location.url,
            arguments: ["ingest", fileURL.path, "--json"] + vaultArgs
        )
        return res.path
    }

    public func ask(query: String) -> AsyncThrowingStream<String, Error> {
        return AsyncThrowingStream { continuation in
            Task {
                do {
                    guard let tool else { throw DeskCoreError.coreNotFound }
                    let process = Process()
                    process.executableURL = tool.location.url
                    process.arguments = ["ask", query, "--json"] + (self.vaultArgs)

                    let pipe = Pipe()
                    process.standardOutput = pipe

                    try process.run()

                    for try await line in pipe.fileHandleForReading.bytes.lines {
                        struct Chunk: Codable, Sendable {
                            let chunk: String
                        }
                        if let data = line.data(using: .utf8),
                           let dec = try? JSONDecoder().decode(Chunk.self, from: data) {
                            continuation.yield(dec.chunk)
                        }
                    }

                    process.waitUntilExit()
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
        }
    }

    // MARK: - Document Library

    public func docsList(status: String? = nil, type: String? = nil, person: String? = nil) async throws -> [DocumentItem] {
        guard let tool else { throw DeskCoreError.coreNotFound }
        var args = ["docs", "list", "--json"] + vaultArgs
        if let s = status, !s.isEmpty { args += ["--status", s] }
        if let t = type, !t.isEmpty { args += ["--type", t] }
        if let p = person, !p.isEmpty { args += ["--person", p] }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            [DocumentItem].self,
            executable: tool.location.url,
            arguments: args
        )
    }

    public func docSetStatus(path: String, status: String) async throws {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        _ = try await runner.runChecked(
            tool.location.url,
            arguments: ["doc", "status", path, status] + vaultArgs
        )
    }

    public func docSetDue(path: String, date: String) async throws {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        _ = try await runner.runChecked(
            tool.location.url,
            arguments: ["doc", "due", path, date] + vaultArgs
        )
    }

    public func docsSimilar(path: String, threshold: Int = 50) async throws -> [SimilarDoc] {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            [SimilarDoc].self,
            executable: tool.location.url,
            arguments: ["similar", path, "--threshold", "\(threshold)", "--json"] + vaultArgs
        )
    }

    public func docsReview(threshold: Int = 70) async throws -> [ReviewDoc] {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        return try await runner.runDecoding(
            [ReviewDoc].self,
            executable: tool.location.url,
            arguments: ["docs", "review", "--threshold", "\(threshold)", "--json"] + vaultArgs
        )
    }

    // MARK: - Document Inspector

    public func docProps(path: String) async throws -> [String: String] {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        let data = try await runner.runChecked(
            tool.location.url,
            arguments: ["props", path, "--json"] + vaultArgs
        )
        if let dict = try? JSONDecoder().decode([String: String].self, from: data) {
            return dict
        }
        return [:]
    }

    public func docNoteContent(path: String) async throws -> String {
        guard let data = FileManager.default.contents(atPath: path) else {
            return ""
        }
        return String(decoding: data, as: UTF8.self)
    }

    public func docSetType(path: String, type: String) async throws {
        try await noteEditProperty(path: path, key: "document_type", value: type)
    }

    public func docSetNoteVisible(path: String, visible: Bool) async throws {
        try await noteEditProperty(path: path, key: "note_visible", value: visible ? "true" : "false")
    }

    public func docSetTags(path: String, tags: String) async throws {
        try await noteEditProperty(path: path, key: "tags", value: tags)
    }

    // MARK: - Vault Setup

    /// Load vault path from VaultConfig on app launch.
    public func loadVaultFromConfig() {
        if let path = VaultConfig.vaultPath() {
            self.vaultPath = path
        }
    }

    public func indexVault(path: String) async throws -> String {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        let data = try await runner.runChecked(
            tool.location.url,
            arguments: ["index", "--json", "--vault", path]
        )
        struct IndexResult: Codable, Sendable {
            let status: String
            let indexed: Int
            let skipped: Int
        }
        let result = try JSONDecoder().decode(IndexResult.self, from: data)
        return "Index complete. \(result.indexed) new/updated files, \(result.skipped) skipped."
    }

    public func initDemo(into demoDir: String) async throws -> String {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        let data = try await runner.runChecked(
            tool.location.url,
            arguments: ["demo", "init", "--json", demoDir]
        )
        struct DemoInitResult: Codable, Sendable {
            let status: String
            let path: String
        }
        let result = try JSONDecoder().decode(DemoInitResult.self, from: data)
        return result.path
    }
}

public struct SimilarDoc: Codable, Equatable, Identifiable, Sendable {
    public var id: String { path }
    public let path: String
    public let title: String
    public let similarity: Int
}

public struct ReviewDoc: Codable, Equatable, Identifiable, Sendable {
    public var id: String { path }
    public let path: String
    public let title: String
    public let confidence: Int
    public let reasons: [String]
}
