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
        case modified
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        path = try container.decode(String.self, forKey: .path)
        title = try container.decode(String.self, forKey: .title)
        sha256 = try container.decodeIfPresent(String.self, forKey: .sha256) ?? ""
        modifiedAt = try container.decodeIfPresent(String.self, forKey: .modifiedAt)
            ?? container.decodeIfPresent(String.self, forKey: .modified)
            ?? ""
        indexedAt = try container.decodeIfPresent(String.self, forKey: .indexedAt) ?? ""
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(path, forKey: .path)
        try container.encode(title, forKey: .title)
        try container.encode(sha256, forKey: .sha256)
        try container.encode(modifiedAt, forKey: .modifiedAt)
        try container.encode(indexedAt, forKey: .indexedAt)
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

public struct SearchResponse: Codable, Equatable, Sendable {
    public let results: [SearchResult]
    public let hint: String?
}

public enum AIEventType: String, Codable, Sendable {
    case answer
    case citation
    case tool
    case done
}

public struct AIEvent: Codable, Equatable, Identifiable, Sendable {
    public var id: UUID { UUID() }
    public let type: AIEventType
    public let text: String?
    public let path: String?
    public let title: String?
    public let snippet: String?
    public let score: Double?
    public let toolName: String?
    public let status: String?

    enum CodingKeys: String, CodingKey {
        case type, text, path, title, snippet, score, status
        case toolName = "tool_name"
    }
}

/// A bounded-error NDJSON line the server appends to a streaming
/// `/api/v1/command` response when the subprocess fails or its output
/// exceeded the size limit — see `internal/selfhost.streamCommand`.
private struct RemoteStreamError: Codable, Sendable {
    let type: String
    let message: String
}

public struct DbFilter: Codable, Equatable, Sendable {
    public let key: String
    public let operatorString: String
    public let value: String

    public init(key: String, operatorString: String = "", value: String = "") {
        self.key = key
        self.operatorString = operatorString
        self.value = value
    }

    enum CodingKeys: String, CodingKey {
        case key
        case operatorString = "operator"
        case value
    }
}

/// A recursive all/any condition group mirroring the core view contract.
public struct DbFilterGroup: Codable, Equatable, Sendable {
    public let operatorString: String
    public let filters: [DbFilter]?
    public let groups: [DbFilterGroup]?

    public init(operatorString: String = "all", filters: [DbFilter]? = nil, groups: [DbFilterGroup]? = nil) {
        self.operatorString = operatorString
        self.filters = filters
        self.groups = groups
    }

    enum CodingKeys: String, CodingKey {
        case operatorString = "operator"
        case filters, groups
    }
}

public struct DbSort: Codable, Equatable, Sendable {
    public let key: String
    public let ascending: Bool

    public init(key: String, ascending: Bool = true) {
        self.key = key
        self.ascending = ascending
    }
}

public struct ComputedColumn: Codable, Equatable, Sendable {
    public let formula: String?
    public let rollup: String?
}

public struct DbViewTemplate: Codable, Equatable, Sendable {
    public let ref: String?
    public let defaults: [String: String]?
}

/// A note that references another note via a frontmatter property or wikilink.
public struct InverseRelation: Codable, Equatable, Identifiable, Sendable {
    public var id: String { source + "#" + property }
    public let source: String
    public let title: String
    public let property: String
}

public struct DbView: Codable, Equatable, Identifiable, Sendable {
    public let id: String
    public let name: String
    public let type: String?
    public let groupBy: String?
    public let dateProperty: String?
    public let computed: [String: ComputedColumn]?
    public let filters: [DbFilter]
    public let filterGroup: DbFilterGroup?
    public let sorts: [DbSort]
    public let columns: [String]
    public let source: String?
    public let template: DbViewTemplate?

    public init(
        id: String,
        name: String,
        type: String? = nil,
        groupBy: String? = nil,
        dateProperty: String? = nil,
        computed: [String: ComputedColumn]? = nil,
        filters: [DbFilter] = [],
        filterGroup: DbFilterGroup? = nil,
        sorts: [DbSort] = [],
        columns: [String] = [],
        source: String? = nil,
        template: DbViewTemplate? = nil
    ) {
        self.id = id
        self.name = name
        self.type = type
        self.groupBy = groupBy
        self.dateProperty = dateProperty
        self.computed = computed
        self.filters = filters
        self.filterGroup = filterGroup
        self.sorts = sorts
        self.columns = columns
        self.source = source
        self.template = template
    }

    enum CodingKeys: String, CodingKey {
        case id, name, type, filters, sorts, columns, computed, source, template
        case groupBy = "group_by"
        case dateProperty = "date_property"
        case filterGroup = "filter_group"
    }
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
    public let asn: Int

    enum CodingKeys: String, CodingKey {
        case path, title, person, status, confidence, correspondent, asn
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
        asn = (try? c.decode(Int.self, forKey: .asn)) ?? 0
    }

    public init(
        path: String,
        title: String,
        documentDate: String,
        person: String,
        status: String,
        dueDate: String,
        confidence: Int,
        correspondent: String,
        documentType: String,
        asn: Int = 0
    ) {
        self.path = path
        self.title = title
        self.documentDate = documentDate
        self.person = person
        self.status = status
        self.dueDate = dueDate
        self.confidence = confidence
        self.correspondent = correspondent
        self.documentType = documentType
        self.asn = asn
    }
}

/// Resolves the vault note and archived original used by the document viewer.
/// `docs list` intentionally returns vault-relative note paths, while ingest
/// metadata may contain absolute, tilde-based, or vault-relative source paths.
public enum DocumentPreviewResolver {
    public static let sourcePropertyKeys = ["archive_path", "source_path", "original_path"]

    public static func noteURL(documentPath: String, vaultPath: String?) -> URL? {
        let path = cleanedPath(documentPath)
        guard !path.isEmpty else { return nil }

        if path.hasPrefix("/") {
            return URL(fileURLWithPath: path).standardizedFileURL
        }
        guard let vaultPath, !vaultPath.isEmpty else { return nil }
        return URL(fileURLWithPath: vaultPath, isDirectory: true)
            .appendingPathComponent(path)
            .standardizedFileURL
    }

    /// Returns the first readable archived original. The durable archive path
    /// wins over the transient source path when both are present.
    public static func sourceURL(
        documentPath: String,
        properties: [String: String],
        vaultPath: String?,
        fileExists: (String) -> Bool = { FileManager.default.fileExists(atPath: $0) }
    ) -> URL? {
        let note = noteURL(documentPath: documentPath, vaultPath: vaultPath)

        for key in sourcePropertyKeys {
            guard let rawPath = properties[key] else { continue }
            for candidate in sourceCandidates(rawPath: rawPath, noteURL: note, vaultPath: vaultPath) {
                if fileExists(candidate.path) {
                    return candidate
                }
            }
        }
        return nil
    }

    private static func sourceCandidates(rawPath: String, noteURL: URL?, vaultPath: String?) -> [URL] {
        let path = cleanedPath(rawPath)
        guard !path.isEmpty else { return [] }

        if let fileURL = URL(string: path), fileURL.isFileURL {
            return [fileURL.standardizedFileURL]
        }

        let expanded = (path as NSString).expandingTildeInPath
        if expanded.hasPrefix("/") {
            return [URL(fileURLWithPath: expanded).standardizedFileURL]
        }

        var candidates: [URL] = []
        if let vaultPath, !vaultPath.isEmpty {
            candidates.append(
                URL(fileURLWithPath: vaultPath, isDirectory: true)
                    .appendingPathComponent(expanded)
                    .standardizedFileURL
            )
        }
        if let noteURL {
            candidates.append(
                noteURL.deletingLastPathComponent()
                    .appendingPathComponent(expanded)
                    .standardizedFileURL
            )
        }

        var seen: Set<String> = []
        return candidates.filter { seen.insert($0.path).inserted }
    }

    private static func cleanedPath(_ value: String) -> String {
        var path = value.trimmingCharacters(in: .whitespacesAndNewlines)
        if path.count >= 2,
           (path.hasPrefix("\"") && path.hasSuffix("\"")
            || path.hasPrefix("'") && path.hasSuffix("'")) {
            path.removeFirst()
            path.removeLast()
        }
        return path.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

/// Converts heterogeneous JSON frontmatter values into readable inspector
/// strings without discarding the whole payload when one value is numeric,
/// boolean, or an array.
public enum DocumentProperties {
    public static func decode(_ data: Data) throws -> [String: String] {
        let values = try JSONDecoder().decode([String: PropertyValue].self, from: data)
        return values.mapValues(\.displayString)
    }

    private enum PropertyValue: Decodable, Sendable {
        case string(String)
        case integer(Int)
        case number(Double)
        case boolean(Bool)
        case array([PropertyValue])
        case object([String: PropertyValue])
        case null

        init(from decoder: Decoder) throws {
            let container = try decoder.singleValueContainer()
            if container.decodeNil() { self = .null }
            else if let value = try? container.decode(String.self) { self = .string(value) }
            else if let value = try? container.decode(Bool.self) { self = .boolean(value) }
            else if let value = try? container.decode(Int.self) { self = .integer(value) }
            else if let value = try? container.decode(Double.self) { self = .number(value) }
            else if let value = try? container.decode([PropertyValue].self) { self = .array(value) }
            else if let value = try? container.decode([String: PropertyValue].self) { self = .object(value) }
            else { throw DecodingError.dataCorruptedError(in: container, debugDescription: "Unsupported property value") }
        }

        var displayString: String {
            switch self {
            case .string(let value): return value
            case .integer(let value): return String(value)
            case .number(let value): return String(value)
            case .boolean(let value): return value ? "true" : "false"
            case .array(let values): return values.map(\.displayString).joined(separator: ", ")
            case .object(let values):
                return values.keys.sorted().map { "\($0): \(values[$0]?.displayString ?? "")" }.joined(separator: ", ")
            case .null: return ""
            }
        }
    }
}

public struct IngestJob: Decodable, Identifiable, Sendable {
    public let id: String
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
        case kind, status, attempts
        case lastError = "last_error"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case sourcePath = "source_path"
		case capability, error
    }

	public init(from decoder: Decoder) throws {
		let c = try decoder.container(keyedBy: CodingKeys.self)
		if let stringID = try? c.decode(String.self, forKey: .id) {
			id = stringID
		} else {
			id = String(try c.decode(Int64.self, forKey: .id))
		}
		documentId = (try? c.decode(Int64.self, forKey: .documentId)) ?? 0
		kind = (try? c.decode(String.self, forKey: .kind))
			?? (try? c.decode(String.self, forKey: .capability)) ?? "ocr"
		status = try c.decode(String.self, forKey: .status)
		attempts = (try? c.decode(Int.self, forKey: .attempts)) ?? 0
		lastError = (try? c.decodeIfPresent(String.self, forKey: .lastError))
			?? (try? c.decodeIfPresent(String.self, forKey: .error)) ?? nil
		createdAt = (try? c.decode(String.self, forKey: .createdAt)) ?? ""
		updatedAt = (try? c.decode(String.self, forKey: .updatedAt)) ?? ""
		sourcePath = try c.decode(String.self, forKey: .sourcePath)
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
	@Published public private(set) var serverURL: URL?

    public var isDemoMode: Bool { VaultConfig.isDemoMode }
	public var isRemote: Bool { remoteClient != nil }
	private var remoteClient: RemoteDeskClient?

	/// The active local-CLI or remote-HTTP transport, set alongside `tool`/
	/// `remoteClient` in `initialize()`. Feature methods route through this
	/// instead of branching on `remoteClient` individually.
	private var transport: DeskTransport?

    private init() {}

    /// Appends `--vault <path>` when a vault is configured, empty otherwise.
    private var vaultArgs: [String] {
        guard let path = vaultPath, !path.isEmpty else { return [] }
        return ["--vault", path]
    }

    public func initialize() async {
		if let connection = ServerConnectionConfig.connection() {
			let client = RemoteDeskClient(connection: connection)
			do {
				let status = try await client.status()
				guard status.schemaVersion == 1 else {
					throw DeskCoreError.schemaMismatch(expected: 1, got: status.schemaVersion)
				}
				remoteClient = client
				transport = RemoteDeskTransport(client: client)
				serverURL = connection.url
				vaultPath = nil
				tool = nil
				isReady = true
				errorMessage = nil
				return
			} catch {
				errorMessage = "Could not connect to SymDesk Server: \(error.localizedDescription)"
				isReady = false
				return
			}
		}
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
            self.transport = LocalDeskTransport(tool: detected)
            self.isReady = true
        } catch {
            self.errorMessage = "symdesk schema mismatch: \(error)"
        }
    }

	public func connectToServer(url: String, token: String) async throws {
		try ServerConnectionConfig.save(url: url, token: token)
		remoteClient = nil
		transport = nil
		serverURL = nil
		tool = nil
		isReady = false
		errorMessage = nil
		await initialize()
		if !isReady {
			ServerConnectionConfig.reset()
			remoteClient = nil
			transport = nil
			serverURL = nil
			throw ServerConnectionError.server(status: 0, message: errorMessage ?? "Connection failed")
		}
	}

	public func disconnectServer() {
		ServerConnectionConfig.reset()
		remoteClient = nil
		transport = nil
		serverURL = nil
		isReady = false
	}

	private func runChecked(arguments: [String], stdin: String = "") async throws -> Data {
		guard let transport else { throw DeskCoreError.coreNotFound }
		return try await transport.command(arguments: arguments, stdin: stdin)
	}

	private func runDecoding<T: Decodable & Sendable>(_ type: T.Type, arguments: [String], stdin: String = "") async throws -> T {
		let data = try await runChecked(arguments: arguments, stdin: stdin)
		return try JSONDecoder().decode(type, from: data)
	}

    public func listFiles() async throws -> [Note] {
		try await runDecoding([Note].self, arguments: ["ls", "--json"] + vaultArgs)
    }

    public func getDoctor() async throws -> String {
		let out = try await runChecked(arguments: ["doctor"] + vaultArgs)
        return String(decoding: out, as: UTF8.self)
    }

    public func search(query: String) async throws -> SearchResponse {
		try await runDecoding(SearchResponse.self, arguments: ["search", query, "--json"] + vaultArgs)
    }

    public func backlinks(for path: String) async throws -> [String] {
		try await runDecoding([String].self, arguments: ["backlinks", path, "--json"] + vaultArgs)
    }

    public func noteNew(title: String, template: String = "") async throws -> String {
        // symdesk note new --title <title> --json
        // Returns e.g. {"path": "..."}
        struct NoteNewResult: Codable, Sendable {
            let path: String
        }
        var args = ["note", "new", "--title", title, "--json"]
        if !template.isEmpty {
            args.append(contentsOf: ["--template", template])
        }
        args.append(contentsOf: vaultArgs)
        
		let res = try await runDecoding(NoteNewResult.self, arguments: args)
        return res.path
    }

    public func noteEditProperty(path: String, key: String, value: String) async throws {
		_ = try await runChecked(arguments: ["props", "edit", path, key, value] + vaultArgs)
    }

    public func getGraph() async throws -> GraphData {
		try await runDecoding(GraphData.self, arguments: ["graph", "--json"] + vaultArgs)
    }

    public func viewsList() async throws -> [DbView] {
		try await runDecoding([DbView].self, arguments: ["views", "list", "--json"] + vaultArgs)
    }

    public func viewsSave(_ view: DbView) async throws {
        let encoder = JSONEncoder()
        let data = try encoder.encode(view)
        let json = String(decoding: data, as: UTF8.self)
		_ = try await runChecked(arguments: ["views", "save", json, "--json"] + vaultArgs)
    }

    public func viewsDelete(id: String) async throws {
		_ = try await runChecked(arguments: ["views", "delete", id, "--json"] + vaultArgs)
    }

    public func relationsInverse(path: String) async throws -> [InverseRelation] {
		try await runDecoding([InverseRelation].self, arguments: ["relations", "inverse", path, "--json"] + vaultArgs)
    }

    public func viewsGet(id: String) async throws -> DbView {
		try await runDecoding(DbView.self, arguments: ["views", "get", id, "--json"] + vaultArgs)
    }

    public func viewsNewEntry(id: String, title: String) async throws -> String {
        struct Result: Codable, Sendable { let path: String }
		return try await runDecoding(Result.self, arguments: ["views", "new-entry", id, title, "--json"] + vaultArgs).path
    }

    public func viewsSiblings(id: String) async throws -> [DbView] {
		try await runDecoding([DbView].self, arguments: ["views", "siblings", id, "--json"] + vaultArgs)
    }

    // We expect an array of JSON objects. In Swift we can use [String: AnyCodable] or just a loose representation.
    // For simplicity, let's use [String: String] since sqlite snippet / properties are strings, or a generic dictionary.
    // However, Swift's Codable doesn't do [String: Any] easily without a custom wrapper.
    // Let's use `Data` and let the UI decode it or use a simple struct.
    public func viewsExec(id: String) async throws -> Data {
        // We runChecked and just return the raw JSON data so the UI can parse it dynamically
		return try await runChecked(arguments: ["views", "exec", id, "--json"] + vaultArgs)
    }
    public func ingest(fileURL: URL) async throws -> String {
		guard let transport else { throw DeskCoreError.coreNotFound }
		return try await transport.ingestFile(fileURL, vaultArgs: vaultArgs)
    }

    public func resolveConflict(path: String, action: String) async throws {
		_ = try await runChecked(arguments: ["conflict", "resolve", path, "--action", action] + vaultArgs)
    }

	public func ingestJobs() async throws -> [IngestJob] {
		guard let transport else { throw DeskCoreError.coreNotFound }
		return try await transport.ingestJobs(vaultArgs: vaultArgs)
    }

	public func ingestRetry(jobID: String) async throws {
		guard let transport else { throw DeskCoreError.coreNotFound }
		try await transport.ingestRetry(jobID: jobID, vaultArgs: vaultArgs)
    }

    public func ask(query: String) -> AsyncThrowingStream<AIEvent, Error> {
        return AsyncThrowingStream { continuation in
            Task {
                guard let transport = self.transport else {
                    continuation.finish(throwing: DeskCoreError.coreNotFound)
                    return
                }
                do {
                    for try await line in transport.commandStream(arguments: ["ask", query, "--json"] + self.vaultArgs, stdin: "") {
                        if let streamError = try? JSONDecoder().decode(RemoteStreamError.self, from: Data(line.utf8)), streamError.type == "error" {
                            throw ServerConnectionError.server(status: 0, message: streamError.message)
                        }
                        if let event = try? JSONDecoder().decode(AIEvent.self, from: Data(line.utf8)) {
                            continuation.yield(event)
                        }
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
        }
    }

    /// Streams an AI transformation (summarize | rewrite | continue) of the
    /// given text. The selection is passed on stdin so multi-line content needs
    /// no escaping; transform never touches the vault, so no vault args are sent.
    public func transform(text: String, intent: String) -> AsyncThrowingStream<String, Error> {
        return AsyncThrowingStream { continuation in
            Task {
                guard let transport = self.transport else {
                    continuation.finish(throwing: DeskCoreError.coreNotFound)
                    return
                }
                do {
                    struct Chunk: Codable, Sendable { let chunk: String }
                    for try await line in transport.commandStream(arguments: ["transform", intent, "--json"], stdin: text) {
                        if let streamError = try? JSONDecoder().decode(RemoteStreamError.self, from: Data(line.utf8)), streamError.type == "error" {
                            throw ServerConnectionError.server(status: 0, message: streamError.message)
                        }
                        if let dec = try? JSONDecoder().decode(Chunk.self, from: Data(line.utf8)) {
                            continuation.yield(dec.chunk)
                        }
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
        }
    }

    // MARK: - Document Library

    public func docsList(status: String? = nil, type: String? = nil, person: String? = nil, asn: Int? = nil) async throws -> [DocumentItem] {
        var args = ["docs", "list", "--json"] + vaultArgs
        if let s = status, !s.isEmpty { args += ["--status", s] }
        if let t = type, !t.isEmpty { args += ["--type", t] }
        if let p = person, !p.isEmpty { args += ["--person", p] }
        if let asn, asn > 0 { args += ["--asn", "\(asn)"] }
		return try await runDecoding([DocumentItem].self, arguments: args)
    }

    public func docSetStatus(path: String, status: String) async throws {
		_ = try await runChecked(arguments: ["doc", "status", path, status] + vaultArgs)
    }

    public func docSetDue(path: String, date: String) async throws {
		_ = try await runChecked(arguments: ["doc", "due", path, date] + vaultArgs)
    }

    // MARK: - Batch document mutations

    /// Per-file outcome of a batch document mutation, mirroring the core's
    /// `doc <mutation> <file...>` JSON payload.
    public struct DocBatchOutcome: Codable, Equatable, Sendable {
        public struct Item: Codable, Equatable, Sendable {
            public let file: String
            public let status: String
            public let error: String?
        }
        public let status: String
        public let updated: Int
        public let failed: Int
        public let results: [Item]
    }

    private func runDocBatch(_ leading: [String], paths: [String], trailing: [String]) async throws -> DocBatchOutcome {
		try await runDecoding(DocBatchOutcome.self, arguments: leading + paths + trailing + ["--json"] + vaultArgs)
    }

    /// Set status on many documents in one core invocation.
    @discardableResult
    public func docSetStatus(paths: [String], status: String) async throws -> DocBatchOutcome {
        try await runDocBatch(["doc", "status"], paths: paths, trailing: [status])
    }

    /// Set due date on many documents in one core invocation.
    @discardableResult
    public func docSetDue(paths: [String], date: String) async throws -> DocBatchOutcome {
        try await runDocBatch(["doc", "due"], paths: paths, trailing: [date])
    }

    /// Set document_type on many documents in one core invocation.
    @discardableResult
    public func docSetType(paths: [String], type: String) async throws -> DocBatchOutcome {
        try await runDocBatch(["doc", "type"], paths: paths, trailing: [type])
    }

    /// Set correspondent on many documents in one core invocation.
    @discardableResult
    public func docSetCorrespondent(paths: [String], name: String) async throws -> DocBatchOutcome {
        try await runDocBatch(["doc", "correspondent"], paths: paths, trailing: [name])
    }

    /// Add a tag to many documents in one core invocation.
    @discardableResult
    public func docAddTag(paths: [String], tag: String) async throws -> DocBatchOutcome {
        try await runDocBatch(["doc", "tag", "add", tag], paths: paths, trailing: [])
    }

    /// Remove a tag from many documents in one core invocation.
    @discardableResult
    public func docRemoveTag(paths: [String], tag: String) async throws -> DocBatchOutcome {
        try await runDocBatch(["doc", "tag", "remove", tag], paths: paths, trailing: [])
    }

    public func docSetASN(path: String, value: String = "next") async throws {
		_ = try await runChecked(arguments: ["doc", "asn", path, value] + vaultArgs)
    }

    public func docsSimilar(path: String, threshold: Int = 50) async throws -> [SimilarDoc] {
		try await runDecoding([SimilarDoc].self, arguments: ["similar", path, "--threshold", "\(threshold)", "--json"] + vaultArgs)
    }

    public func docsReview(threshold: Int = 70) async throws -> [ReviewDoc] {
		try await runDecoding([ReviewDoc].self, arguments: ["docs", "review", "--threshold", "\(threshold)", "--json"] + vaultArgs)
    }

    // MARK: - Document Inspector

    public func docProps(path: String) async throws -> [String: String] {
		let data = try await runChecked(arguments: ["props", "get", path, "--json"] + vaultArgs)
        return try DocumentProperties.decode(data)
    }

    public func docNoteContent(path: String) async throws -> String {
		guard let transport else { throw DeskCoreError.coreNotFound }
		return try await transport.fileContent(path: path)
    }

	public func saveNoteContent(path: String, content: String) async throws {
		guard let transport else { throw DeskCoreError.coreNotFound }
		try await transport.saveFile(path: path, content: content)
	}

	public func remoteCachedFile(path: String) async throws -> URL {
		guard let remoteClient else { throw ServerConnectionError.missingConfiguration }
		return try await remoteClient.cachedFile(path: path)
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
    public let status: String
    public let documentType: String

    enum CodingKeys: String, CodingKey {
        case path, title, status, confidence, reasons
        case documentType = "document_type"
    }

    public let confidence: Int
    public let reasons: [String]

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        title = try c.decode(String.self, forKey: .title)
        status = (try? c.decode(String.self, forKey: .status)) ?? ""
        documentType = (try? c.decode(String.self, forKey: .documentType)) ?? ""
        confidence = (try? c.decode(Int.self, forKey: .confidence)) ?? 0
        reasons = (try? c.decode([String].self, forKey: .reasons)) ?? []
    }
}

// MARK: - Doctor Report

public struct DoctorReport: Codable, Sendable {
    public let overall: String
    public let vault: ToolAvailability
    public let sidecar: ToolAvailability
    public let tools: ToolAvailability

    public struct ToolAvailability: Codable, Sendable {
        public let symseek: String?
        public let symmemory: String?
        public let symingest: String?
        public let symfetch: String?
        public let symvault: String?

        public init(symseek: String? = nil, symmemory: String? = nil, symingest: String? = nil, symfetch: String? = nil, symvault: String? = nil) {
            self.symseek = symseek
            self.symmemory = symmemory
            self.symingest = symingest
            self.symfetch = symfetch
            self.symvault = symvault
        }

        /// Whether a tool name resolves to "ok" or "available".
        public func isAvailable(_ name: String) -> Bool {
            let val: String?
            switch name {
            case "symseek": val = symseek
            case "symmemory": val = symmemory
            case "symingest": val = symingest
            case "symfetch": val = symfetch
            case "symvault": val = symvault
            default: val = nil
            }
            guard let v = val else { return false }
            let lower = v.lowercased()
            return lower == "ok" || lower == "available" || lower == "found"
        }
    }

    public init(overall: String, vault: ToolAvailability, sidecar: ToolAvailability, tools: ToolAvailability) {
        self.overall = overall
        self.vault = vault
        self.sidecar = sidecar
        self.tools = tools
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        overall = (try? c.decode(String.self, forKey: .overall)) ?? "unknown"
        vault = (try? c.decode(ToolAvailability.self, forKey: .vault)) ?? ToolAvailability()
        sidecar = (try? c.decode(ToolAvailability.self, forKey: .sidecar)) ?? ToolAvailability()
        tools = (try? c.decode(ToolAvailability.self, forKey: .tools)) ?? ToolAvailability()
    }
}

// MARK: - Meetings

extension DeskCore {
    /// Lists every meeting note already imported into the vault. Unlike
    /// `meetingsAvailable`, this always works even when `symmeet` is not on
    /// PATH, since it only walks already-written vault notes.
    public func meetingsList() async throws -> [MeetingNoteSummary] {
        try await runDecoding([MeetingNoteSummary].self, arguments: ["meeting", "list", "--json"] + vaultArgs)
    }

    /// Lists SymMeet meetings that have not yet been imported, for an
    /// "Import Existing SymMeet Meeting" picker. Throws when `symmeet` is
    /// not on PATH; callers should surface that as "unavailable", not as
    /// note corruption.
    public func meetingsAvailable() async throws -> [AvailableMeeting] {
        try await runDecoding([AvailableMeeting].self, arguments: ["meeting", "available", "--json"] + vaultArgs)
    }

    /// Imports a SymMeet meeting into the vault and returns the new note's
    /// vault-relative path.
    @discardableResult
    public func meetingImport(meetingID: String) async throws -> String {
        struct ImportResult: Codable, Sendable { let path: String }
        let result = try await runDecoding(ImportResult.self, arguments: ["meeting", "import", meetingID, "--json"] + vaultArgs)
        return result.path
    }

    /// Loads one imported meeting note's metadata and transcript body.
    public func meetingShow(path: String) async throws -> MeetingDetail {
        try await runDecoding(MeetingDetail.self, arguments: ["meeting", "show", path, "--json"] + vaultArgs)
    }

    /// Previews (or, with `apply: true`, writes) a re-export of a meeting
    /// note's transcript from SymMeet.
    public func meetingRefresh(path: String, apply: Bool) async throws -> MeetingRefreshOutcome {
        var args = ["meeting", "refresh", path, "--json"] + vaultArgs
        if apply { args.append("--apply") }
        return try await runDecoding(MeetingRefreshOutcome.self, arguments: args)
    }

    /// Loads the time-coded transcript segments of a meeting note's source
    /// artifact, for the synchronized review timeline. Throws when `symmeet`
    /// is absent or the artifact is gone; callers show that as
    /// "unavailable", never as note corruption.
    public func meetingSegments(path: String) async throws -> [MeetingSegment] {
        try await runDecoding([MeetingSegment].self, arguments: ["meeting", "segments", path, "--json"] + vaultArgs)
    }

    /// Lists the speakers of a meeting note's source artifact with their
    /// current display labels from the symmeet edit layer.
    public func meetingSpeakers(path: String) async throws -> [MeetingSpeaker] {
        try await runDecoding([MeetingSpeaker].self, arguments: ["meeting", "speakers", path, "--json"] + vaultArgs)
    }

    /// Assigns a display label to an anonymous speaker in the source
    /// artifact's edit layer.
    public func meetingSpeakerLabel(path: String, speakerID: String, label: String) async throws {
        _ = try await runDecoding([String: String].self, arguments: ["meeting", "speaker", "label", path, speakerID, label, "--json"] + vaultArgs)
    }

    /// Merges one speaker into another in the source artifact's edit layer.
    public func meetingSpeakerMerge(path: String, fromSpeakerID: String, toSpeakerID: String) async throws {
        _ = try await runDecoding([String: String].self, arguments: ["meeting", "speaker", "merge", path, fromSpeakerID, toSpeakerID, "--json"] + vaultArgs)
    }

    /// Splits a segment away from its current speaker in the source
    /// artifact's edit layer.
    public func meetingSpeakerSplit(path: String, speakerID: String, segmentID: String) async throws {
        _ = try await runDecoding([String: String].self, arguments: ["meeting", "speaker", "split", path, speakerID, "--segment", segmentID, "--json"] + vaultArgs)
    }

    /// Discards all speaker edits for the source meeting, restoring raw
    /// engine output.
    public func meetingSpeakerReset(path: String) async throws {
        _ = try await runDecoding([String: String].self, arguments: ["meeting", "speaker", "reset", path, "--json"] + vaultArgs)
    }

    /// Marks a meeting note as reviewed. The CLI snapshots the previous
    /// note content to history before writing, so a review save is always
    /// recoverable.
    public func meetingMarkReviewed(path: String) async throws {
        _ = try await runDecoding([String: String].self, arguments: ["meeting", "review", path, "--json"] + vaultArgs)
    }

    /// Lists deterministic Memory person candidates for a participant
    /// label. Every candidate is an exact-name or alias match with its
    /// match reason — never a fuzzy guess.
    public func meetingParticipantCandidates(label: String) async throws -> [ParticipantCandidate] {
        try await runDecoding([ParticipantCandidate].self, arguments: ["meeting", "participant", "candidates", label, "--json"] + vaultArgs)
    }

    /// Links a speaker to a confirmed Memory entity; a `nil` or empty
    /// entity ID unlinks the participant (back to anonymous).
    public func meetingParticipantConfirm(path: String, speakerID: String, entityID: String?) async throws {
        var args = ["meeting", "participant", "confirm", path, speakerID]
        if let entityID, !entityID.isEmpty { args.append(entityID) }
        args.append("--json")
        _ = try await runDecoding([String: String].self, arguments: args + vaultArgs)
    }

    /// Creates a confirmed new Memory person (reviewer-typed name) and
    /// links the speaker to it, returning the new entity ID.
    @discardableResult
    public func meetingParticipantCreate(path: String, speakerID: String, name: String) async throws -> String {
        let result = try await runDecoding([String: String].self, arguments: ["meeting", "participant", "create", path, speakerID, name, "--json"] + vaultArgs)
        return result["entity_id"] ?? ""
    }

    /// Publishes a reviewed proposal (confirmed-participant relations plus
    /// the given facts) to Symaira Memory. Repeat applies are idempotent:
    /// already-published facts are skipped, relations are idempotent.
    public func meetingPublish(path: String, facts: [String]) async throws -> MeetingPublishOutcome {
        var args = ["meeting", "publish", path]
        for fact in facts {
            args.append(contentsOf: ["--fact", fact])
        }
        args.append("--json")
        return try await runDecoding(MeetingPublishOutcome.self, arguments: args + vaultArgs)
    }
}

/// One deterministic Memory person candidate for a participant label;
/// mirrors the Go `service.ParticipantCandidate` JSON shape.
public struct ParticipantCandidate: Codable, Identifiable, Sendable, Equatable {
    public var id: String { entityID }
    public let entityID: String
    public let name: String
    public let matchReason: String

    enum CodingKeys: String, CodingKey {
        case entityID = "entity_id"
        case name
        case matchReason = "match_reason"
    }

    public init(entityID: String, name: String, matchReason: String) {
        self.entityID = entityID
        self.name = name
        self.matchReason = matchReason
    }
}

/// The result of one reviewed Memory publish; mirrors the Go
/// `service.MeetingPublishResult` JSON shape.
public struct MeetingPublishOutcome: Codable, Sendable, Equatable {
    public let meetingEntityID: String
    public let relationsCreated: Int
    public let factsPublished: [String]?
    public let factsSkipped: Int

    enum CodingKeys: String, CodingKey {
        case meetingEntityID = "meeting_entity_id"
        case relationsCreated = "relations_created"
        case factsPublished = "facts_published"
        case factsSkipped = "facts_skipped"
    }

    public init(meetingEntityID: String, relationsCreated: Int, factsPublished: [String]?, factsSkipped: Int) {
        self.meetingEntityID = meetingEntityID
        self.relationsCreated = relationsCreated
        self.factsPublished = factsPublished
        self.factsSkipped = factsSkipped
    }
}

/// One time-coded transcript segment of a meeting's source artifact;
/// mirrors the Go `compose.MeetingSegment` JSON shape. `editedText`, when
/// present, is the user-corrected text that supersedes `engineText`.
public struct MeetingSegment: Codable, Identifiable, Sendable, Equatable {
    public var id: String { segmentID }
    public let segmentID: String
    public let speakerID: String
    public let startMS: Int64
    public let endMS: Int64
    public let engineText: String
    public let editedText: String?
    public let revision: String

    /// The text to display: the user correction when one exists, otherwise
    /// the raw engine output.
    public var displayText: String {
        if let editedText, !editedText.isEmpty { return editedText }
        return engineText
    }

    enum CodingKeys: String, CodingKey {
        case segmentID = "segment_id"
        case speakerID = "speaker_id"
        case startMS = "start_ms"
        case endMS = "end_ms"
        case engineText = "engine_text"
        case editedText = "edited_text"
        case revision
    }

    public init(segmentID: String, speakerID: String, startMS: Int64, endMS: Int64, engineText: String, editedText: String? = nil, revision: String = "engine") {
        self.segmentID = segmentID
        self.speakerID = speakerID
        self.startMS = startMS
        self.endMS = endMS
        self.engineText = engineText
        self.editedText = editedText
        self.revision = revision
    }
}

/// One speaker of a meeting's source artifact with its current display
/// label; mirrors the Go `service.MeetingSpeaker` JSON shape.
public struct MeetingSpeaker: Codable, Identifiable, Sendable, Equatable {
    public var id: String { speakerID }
    public let speakerID: String
    public let label: String

    enum CodingKeys: String, CodingKey {
        case speakerID = "speaker_id"
        case label
    }

    public init(speakerID: String, label: String) {
        self.speakerID = speakerID
        self.label = label
    }
}

/// One vault note already imported from a SymMeet meeting; mirrors the Go
/// `service.MeetingNoteSummary` JSON shape.
public struct MeetingNoteSummary: Codable, Equatable, Identifiable, Sendable {
    public var id: String { path }
    public let path: String
    public let title: String
    public let meetingID: String
    public let startedAt: String
    public let durationMS: Int64
    public let language: String
    public let reviewState: String

    enum CodingKeys: String, CodingKey {
        case path, title, language
        case meetingID = "meeting_id"
        case startedAt = "started_at"
        case durationMS = "duration_ms"
        case reviewState = "review_state"
    }

    public init(path: String, title: String, meetingID: String, startedAt: String, durationMS: Int64, language: String, reviewState: String) {
        self.path = path
        self.title = title
        self.meetingID = meetingID
        self.startedAt = startedAt
        self.durationMS = durationMS
        self.language = language
        self.reviewState = reviewState
    }
}

/// A raw SymMeet meeting not yet imported into the vault; mirrors the Go
/// `service.AvailableMeetingSummary` JSON shape.
public struct AvailableMeeting: Codable, Equatable, Identifiable, Sendable {
    public var id: String { meetingID }
    public let meetingID: String
    public let source: String

    enum CodingKeys: String, CodingKey {
        case meetingID = "meeting_id"
        case source
    }

    public init(meetingID: String, source: String) {
        self.meetingID = meetingID
        self.source = source
    }
}

/// One reviewed participant entry on a meeting note; mirrors the Go
/// `service.MeetingParticipant` YAML/JSON shape (see VAULT.md section 8).
/// `entityID` is only ever populated by the separate, explicitly reviewed
/// participant-confirmation flow, never automatically.
public struct MeetingParticipant: Codable, Equatable, Sendable, Identifiable {
    /// Stable identity for UI lists/sheets: the first (meeting-local)
    /// speaker ID, which import guarantees to be unique per participant.
    public var id: String { speakerIDs.first ?? label }
    public let label: String
    public let speakerIDs: [String]
    public let entityID: String?

    enum CodingKeys: String, CodingKey {
        case label
        case speakerIDs = "speaker_ids"
        case entityID = "entity_id"
    }

    public init(label: String, speakerIDs: [String], entityID: String? = nil) {
        self.label = label
        self.speakerIDs = speakerIDs
        self.entityID = entityID
    }
}

/// SymMeet artifact provenance recorded on a meeting note.
public struct MeetingSourceInfo: Codable, Equatable, Sendable {
    public let artifactSchemaVersion: Int
    public let reviewState: String

    enum CodingKeys: String, CodingKey {
        case artifactSchemaVersion = "artifact_schema_version"
        case reviewState = "review_state"
    }

    public init(artifactSchemaVersion: Int, reviewState: String) {
        self.artifactSchemaVersion = artifactSchemaVersion
        self.reviewState = reviewState
    }
}

/// The frontmatter fields of an imported meeting note this app understands.
/// Fields beyond `meetingID`/`startedAt` are optional so a note with a
/// partially-populated or as-yet-unreviewed frontmatter still decodes
/// instead of failing the whole detail load.
public struct MeetingFrontmatter: Codable, Equatable, Sendable {
    public let meetingID: String
    public let startedAt: String
    public let endedAt: String?
    public let durationMS: Int64?
    public let language: String?
    public let participants: [MeetingParticipant]?
    public let symmeetSource: MeetingSourceInfo?

    enum CodingKeys: String, CodingKey {
        case meetingID = "meeting_id"
        case startedAt = "started_at"
        case endedAt = "ended_at"
        case durationMS = "duration_ms"
        case language
        case participants
        case symmeetSource = "symmeet_source"
    }

    public init(meetingID: String, startedAt: String, endedAt: String? = nil, durationMS: Int64? = nil, language: String? = nil, participants: [MeetingParticipant]? = nil, symmeetSource: MeetingSourceInfo? = nil) {
        self.meetingID = meetingID
        self.startedAt = startedAt
        self.endedAt = endedAt
        self.durationMS = durationMS
        self.language = language
        self.participants = participants
        self.symmeetSource = symmeetSource
    }
}

/// One imported meeting note's detail: metadata plus the transcript body,
/// which embeds the reviewed transcript between symmeet-transcript markers
/// (see VAULT.md section 8). Mirrors `vault.Document`'s default (untagged)
/// Go JSON marshaling, so `CodingKeys` use the Go field names verbatim.
public struct MeetingDetail: Codable, Sendable {
    public let title: String
    public let body: String
    public let frontmatter: MeetingFrontmatter

    enum CodingKeys: String, CodingKey {
        case title = "Title"
        case body = "Body"
        case frontmatter = "Frontmatter"
    }

    public init(title: String, body: String, frontmatter: MeetingFrontmatter) {
        self.title = title
        self.body = body
        self.frontmatter = frontmatter
    }
}

/// Mirrors the Go `service.MeetingRefreshResult` JSON shape.
public struct MeetingRefreshOutcome: Codable, Sendable {
    public let path: String
    public let changed: Bool
    public let diffLines: [String]?
    public let applied: Bool

    enum CodingKeys: String, CodingKey {
        case path, changed, applied
        case diffLines = "diff_lines"
    }

    public init(path: String, changed: Bool, diffLines: [String]? = nil, applied: Bool) {
        self.path = path
        self.changed = changed
        self.diffLines = diffLines
        self.applied = applied
    }
}
