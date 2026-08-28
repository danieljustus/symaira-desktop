import Foundation
import SymairaToolKit
import SymairaCLIRunner

public enum DeskCoreError: Error {
    case coreNotFound
    case schemaMismatch(expected: Int, got: Int)
    case cliExecutionFailed(exitCode: Int32, stderr: String)
}

public struct Note: Codable, Equatable, Identifiable, Hashable, Sendable {
    public var id: String { path }
    public let path: String
    public let title: String
    public let sha256: String
    public let modifiedAt: String
    public let indexedAt: String
    public let type: String

    enum CodingKeys: String, CodingKey {
        case path, title, sha256, type
        case modifiedAt = "modified_at"
        case indexedAt = "indexed_at"
        case modified
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        path = try container.decode(String.self, forKey: .path)
        title = try container.decode(String.self, forKey: .title)
        sha256 = try container.decodeIfPresent(String.self, forKey: .sha256) ?? ""
        type = (try? container.decodeIfPresent(String.self, forKey: .type)) ?? ""
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
        try container.encode(type, forKey: .type)
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

/// Snapshot of the hybrid search index and the embedding backend behind it.
public struct RetrievalStatus: Codable, Equatable, Sendable {
    public let documentCount: Int
    public let chunkCount: Int
    public let databaseBytes: Int64
    public let lastIndexedAt: String?
    public let embeddingModel: String
    /// False means the configured backend did not answer and queries fall
    /// back to the local hash embedding: search still returns, but ranking is
    /// degraded. This is deliberately distinct from an empty index.
    public let backendAvailable: Bool
    /// Persisted index degradation, distinct from the live backend probe.
    /// Optional fields keep the app compatible with an older local binary.
    public let pendingChunkCount: Int?
    public let mixedEmbeddingSpaces: Bool?
    /// The retrieval database is currently shared across vaults rather than
    /// scoped to the active vault. Keep this explicit so global figures are
    /// never mistaken for active-vault counts.
    public let indexScope: String?
    /// Number of Markdown files in the active vault, when the CLI was given a
    /// vault path and could enumerate it. This is a comparison figure, not an
    /// assertion that the shared retrieval index contains only this vault.
    public let vaultDocumentCount: Int?

    public var isEmpty: Bool { documentCount == 0 }
    public var hasStoredDegradation: Bool {
        (pendingChunkCount ?? 0) > 0 || mixedEmbeddingSpaces == true
    }

    enum CodingKeys: String, CodingKey {
        case documentCount = "document_count"
        case chunkCount = "chunk_count"
        case databaseBytes = "database_bytes"
        case lastIndexedAt = "last_indexed_at"
        case embeddingModel = "embedding_model"
        case backendAvailable = "backend_available"
        case pendingChunkCount = "pending_chunk_count"
        case mixedEmbeddingSpaces = "mixed_embedding_spaces"
        case indexScope = "index_scope"
        case vaultDocumentCount = "vault_document_count"
    }
}

/// Result of a vault re-index run.
public struct ReindexResult: Codable, Equatable, Sendable {
    public let status: String
    public let indexed: Int
    public let skipped: Int
    public let pruned: Int?
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
    /// Agentic loop (issue #317): iteration number of a tool call/result.
    public let iteration: Int?
    /// Agentic loop: raw JSON inputs of the requested tool call, kept as a
    /// compact JSON string for display (the wire format is an object).
    public let toolInputs: String?
    /// Agentic loop: tool output for a tool-result event.
    public let toolOutput: String?
    /// Agentic loop: whether the tool output was truncated for the model.
    public let toolOutputTruncated: Bool?
    /// Terminal event: cumulative token usage of the whole run.
    public let tokenUsage: Int?
    /// Terminal event: the model's context window in tokens (0 when unknown).
    public let contextWindow: Int?

    enum CodingKeys: String, CodingKey {
        case type, text, path, title, snippet, score, status, iteration
        case toolName = "tool_name"
        case toolInputs = "tool_inputs"
        case toolOutput = "tool_output"
        case toolOutputTruncated = "tool_output_truncated"
        case tokenUsage = "token_usage"
        case contextWindow = "context_window"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        type = try c.decode(AIEventType.self, forKey: .type)
        text = try c.decodeIfPresent(String.self, forKey: .text)
        path = try c.decodeIfPresent(String.self, forKey: .path)
        title = try c.decodeIfPresent(String.self, forKey: .title)
        snippet = try c.decodeIfPresent(String.self, forKey: .snippet)
        score = try c.decodeIfPresent(Double.self, forKey: .score)
        toolName = try c.decodeIfPresent(String.self, forKey: .toolName)
        status = try c.decodeIfPresent(String.self, forKey: .status)
        iteration = try c.decodeIfPresent(Int.self, forKey: .iteration)
        toolOutput = try c.decodeIfPresent(String.self, forKey: .toolOutput)
        toolOutputTruncated = try c.decodeIfPresent(Bool.self, forKey: .toolOutputTruncated)
        tokenUsage = try c.decodeIfPresent(Int.self, forKey: .tokenUsage)
        contextWindow = try c.decodeIfPresent(Int.self, forKey: .contextWindow)
        if c.contains(.toolInputs) {
            let box = try c.decode(JSONValue.self, forKey: .toolInputs)
            if let data = try? JSONEncoder().encode(box) {
                toolInputs = String(data: data, encoding: .utf8)
            } else {
                toolInputs = nil
            }
        } else {
            toolInputs = nil
        }
    }
}

/// Decodes an arbitrary JSON value (any shape) so it can be re-encoded as a
/// compact string for display.
private enum JSONValue: Codable, Sendable {
    case string(String)
    case bool(Bool)
    case int(Int)
    case double(Double)
    case array([JSONValue])
    case object([String: JSONValue])
    case null

    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if let v = try? c.decode(String.self) {
            self = .string(v)
        } else if let v = try? c.decode(Bool.self) {
            self = .bool(v)
        } else if let v = try? c.decode(Int.self) {
            self = .int(v)
        } else if let v = try? c.decode(Double.self) {
            self = .double(v)
        } else if let v = try? c.decode([JSONValue].self) {
            self = .array(v)
        } else if let v = try? c.decode([String: JSONValue].self) {
            self = .object(v)
        } else if c.decodeNil() {
            self = .null
        } else {
            throw DecodingError.dataCorruptedError(in: c, debugDescription: "unsupported JSON value")
        }
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .string(let v): try c.encode(v)
        case .bool(let v): try c.encode(v)
        case .int(let v): try c.encode(v)
        case .double(let v): try c.encode(v)
        case .array(let v): try c.encode(v)
        case .object(let v): try c.encode(v)
        case .null: try c.encodeNil()
        }
    }
}

/// A bounded-error NDJSON line the server appends to a streaming
/// `/api/v1/command` response when the subprocess fails or its output
/// exceeded the size limit — see `internal/selfhost.streamCommand`.
struct RemoteStreamError: Codable, Sendable {
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

public struct PropertyConfig: Codable, Equatable, Sendable {
    public let type: String?
    public let label: String?
    public let options: [String]?
    public let description: String?
    public let `default`: String?

    public init(
        type: String? = nil,
        label: String? = nil,
        options: [String]? = nil,
        description: String? = nil,
        default: String? = nil
    ) {
        self.type = type
        self.label = label
        self.options = options
        self.description = description
        self.default = `default`
    }
}

public struct DbBase: Codable, Equatable, Identifiable, Sendable {
    public let id: String
    public let path: String
    public let title: String
    public let description: String?
    public let created: String?
    public let tags: [String]?
    public let properties: [String: PropertyConfig]?
    public let views: [DbView]

    public init(
        id: String,
        path: String,
        title: String,
        description: String? = nil,
        created: String? = nil,
        tags: [String]? = nil,
        properties: [String: PropertyConfig]? = nil,
        views: [DbView] = []
    ) {
        self.id = id
        self.path = path
        self.title = title
        self.description = description
        self.created = created
        self.tags = tags
        self.properties = properties
        self.views = views
    }
}

public struct BaseEmbedResult: Codable, Equatable, Sendable {
    public let baseID: String
    public let basePath: String
    public let baseTitle: String
    public let viewID: String
    public let viewName: String
    public let columns: [String]
    public let totalRows: Int
    public let capped: Bool
    public let rowCap: Int
    public let markdown: String

    enum CodingKeys: String, CodingKey {
        case baseID = "base_id"
        case basePath = "base_path"
        case baseTitle = "base_title"
        case viewID = "view_id"
        case viewName = "view_name"
        case columns
        case totalRows = "total_rows"
        case capped
        case rowCap = "row_cap"
        case markdown
    }
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
    public let type: String
    public let documentDate: String
    public let person: String
    public let status: String
    public let dueDate: String
    public let confidence: Int
    public let correspondent: String
    public let documentType: String
    public let asn: Int
    /// Tags carried by the document, parsed from its frontmatter tags
    /// property. Empty when the document has no tags (issue #306).
    public let tags: [String]

    enum CodingKeys: String, CodingKey {
        case path, title, type, person, status, confidence, correspondent, asn, tags
        case documentDate = "document_date"
        case dueDate = "due_date"
        case documentType = "document_type"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        title = try c.decode(String.self, forKey: .title)
        type = (try? c.decodeIfPresent(String.self, forKey: .type)) ?? ""
        documentDate = (try? c.decode(String.self, forKey: .documentDate)) ?? ""
        person = (try? c.decode(String.self, forKey: .person)) ?? ""
        status = (try? c.decode(String.self, forKey: .status)) ?? ""
        dueDate = (try? c.decode(String.self, forKey: .dueDate)) ?? ""
        confidence = (try? c.decode(Int.self, forKey: .confidence)) ?? 0
        correspondent = (try? c.decode(String.self, forKey: .correspondent)) ?? ""
        documentType = (try? c.decode(String.self, forKey: .documentType)) ?? ""
        asn = (try? c.decode(Int.self, forKey: .asn)) ?? 0
        tags = (try? c.decodeIfPresent([String].self, forKey: .tags)) ?? []
    }

    public init(
        path: String,
        title: String,
        type: String = "",
        documentDate: String,
        person: String,
        status: String,
        dueDate: String,
        confidence: Int,
        correspondent: String,
        documentType: String,
        asn: Int = 0,
        tags: [String] = []
    ) {
        self.path = path
        self.title = title
        self.type = type
        self.documentDate = documentDate
        self.person = person
        self.status = status
        self.dueDate = dueDate
        self.confidence = confidence
        self.correspondent = correspondent
        self.documentType = documentType
        self.asn = asn
        self.tags = tags
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

/// A page of ingest jobs. The explicit envelope lets clients display the
/// total queue size while older servers/CLIs can still return a top-level array.
public struct IngestJobPage: Decodable, Sendable {
    public let jobs: [IngestJob]
    public let total: Int
    public let limit: Int
    public let offset: Int

    enum CodingKeys: String, CodingKey {
        case jobs, total, limit, offset
    }

    public init(jobs: [IngestJob], total: Int, limit: Int, offset: Int) {
        self.jobs = jobs
        self.total = total
        self.limit = limit
        self.offset = offset
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        jobs = try c.decode([IngestJob].self, forKey: .jobs)
        total = (try? c.decode(Int.self, forKey: .total)) ?? jobs.count
        limit = (try? c.decode(Int.self, forKey: .limit)) ?? jobs.count
        offset = (try? c.decode(Int.self, forKey: .offset)) ?? 0
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
    public let fileType: String?

    public init(id: String, label: String, status: DocumentStatus?, fileType: String? = nil) {
        self.id = id
        self.label = label
        self.status = status
        self.fileType = fileType
    }

    /// The number to show beside this preset.
    ///
    /// Callers used to branch on `status` alone, so every preset without one —
    /// Notes, Documents and Meetings — displayed the vault total and a vault
    /// with no meetings still reported "Meetings 15" (issue #440). Resolve a
    /// type preset against the per-type tally instead; only the preset with
    /// neither key means "everything".
    public func displayCount(statusCounts: [String: Int], typeCounts: [String: Int], total: Int) -> Int {
        if let status {
            return statusCounts[status.rawValue] ?? 0
        }
        if let fileType {
            return typeCounts[fileType] ?? 0
        }
        return total
    }

    public static let defaults: [DocFilterPreset] = [
        .init(id: "all", label: "All Documents", status: nil, fileType: nil),
        .init(id: "open", label: "Open", status: .open, fileType: nil),
        .init(id: "needs_review", label: "Needs Review", status: .needsReview, fileType: nil),
        .init(id: "waiting_for_reply", label: "Waiting for Reply", status: .waitingForReply, fileType: nil),
        .init(id: "submitted", label: "Submitted", status: .submitted, fileType: nil),
        .init(id: "done", label: "Done", status: .done, fileType: nil),
        .init(id: "paid", label: "Paid", status: .paid, fileType: nil),
        .init(id: "notes", label: "Notes", status: nil, fileType: "note"),
        .init(id: "documents", label: "Documents", status: nil, fileType: "document"),
        .init(id: "meetings", label: "Meetings", status: nil, fileType: "meeting"),
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

/// Context captured from the active SymDesk work surface for an AI request.
///
/// The context is opt-in at the dock. It identifies the active surface without
/// silently sending an entire document to a remote provider.
public struct DeskChatContext: Equatable, Sendable {
    public let activeDocument: String?
    public let selectionText: String?
    public let visibleExcerpt: String?
    public let scope: String?
    public let recentDocuments: [String]

    public init(
        activeDocument: String? = nil,
        selectionText: String? = nil,
        visibleExcerpt: String? = nil,
        scope: String? = nil,
        recentDocuments: [String] = []
    ) {
        self.activeDocument = Self.normalized(activeDocument)
        self.selectionText = Self.normalized(selectionText)
        self.visibleExcerpt = Self.normalized(visibleExcerpt)
        self.scope = Self.normalized(scope)
        self.recentDocuments = Array(
            recentDocuments.compactMap(Self.normalized).reduce(into: [String]()) { result, path in
                if !result.contains(path) { result.append(path) }
            }.prefix(4)
        )
    }

    public var isEmpty: Bool {
        activeDocument == nil && selectionText == nil && visibleExcerpt == nil
            && scope == nil && recentDocuments.isEmpty
    }

    public var summary: String {
        if let activeDocument {
            return URL(fileURLWithPath: activeDocument).lastPathComponent
        }
        return scope ?? "Context"
    }

    /// Returns a Unicode-safe excerpt bounded to `limit` characters. When a
    /// selection is available, the excerpt is centered around its first
    /// occurrence so the visible context and selection travel together.
    public static func boundedExcerpt(
        _ text: String,
        around selection: String? = nil,
        limit: Int = 2_400
    ) -> String {
        let source = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard limit > 0, !source.isEmpty else { return "" }
        guard source.count > limit else { return source }

        let marker = "… [truncated]"
        guard limit > marker.count * 2 else { return String(source.prefix(limit)) }

        if let selection = normalized(selection), selection.count < limit - marker.count * 2,
           let range = source.range(of: selection) {
            let available = limit - marker.count * 2 - selection.count
            let leading = available / 2
            let trailing = available - leading
            // `range` is Range<String.Index>; the indices below stay
            // String.Index so the slice is Unicode-safe on every toolchain
            // (an older Xcode release toolchain rejects mixed Int/Index
            // subscripts on this line).
            let lower: String.Index = range.lowerBound
            let upper: String.Index = range.upperBound
            let start = source.index(
                lower,
                offsetBy: -min(leading, source.distance(from: source.startIndex, to: lower))
            )
            let end = source.index(
                upper,
                offsetBy: min(trailing, source.distance(from: upper, to: source.endIndex))
            )
            return marker + String(source[start..<end]) + marker
        }

        return String(source.prefix(limit - marker.count)) + marker
    }

    /// Builds a reference-only prompt section. Note content is wrapped as
    /// context so instructions inside a document are not mistaken for the
    /// user's new request.
    public func prompt(for query: String) -> String {
        let question = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !isEmpty else { return question }

        var lines = ["[Current SymDesk context — reference only]"]
        if let scope { lines.append("Scope: \(scope)") }
        if let activeDocument { lines.append("Active document: \(activeDocument)") }
        if let selectionText {
            lines.append("Editor selection:\n\(Self.indented(selectionText))")
        }
        if let visibleExcerpt {
            lines.append("Visible excerpt:\n\(Self.indented(visibleExcerpt))")
        }
        if !recentDocuments.isEmpty {
            lines.append("Recent documents: \(recentDocuments.joined(separator: ", "))")
        }
        lines.append("[End current context]")
        lines.append("")
        lines.append(question)
        return lines.joined(separator: "\n")
    }

    private static func normalized(_ value: String?) -> String? {
        guard let value else { return nil }
        let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }

    private static func indented(_ value: String) -> String {
        value.split(separator: "\n", omittingEmptySubsequences: false)
            .map { "  \($0)" }
            .joined(separator: "\n")
    }
}

/// Compatibility name for callers from the first implementation pass.
public typealias AIDockContext = DeskChatContext

@MainActor
public final class DeskCore: ObservableObject {
    public static let shared = DeskCore()

    /// The managed runtime directory (`~/.symaira/bin`) is checked before
    /// Homebrew prefixes. An absent directory simply falls through (#459).
    private static let managedRuntimeDir: String = {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        return "\(home)/.symaira/bin"
    }()

    private lazy var locator: BinaryLocator = {
        var loc = BinaryLocator(bundle: Bundle.main)
        // Prepend managed runtime so it is checked before Homebrew/PATH.
        // The full priority reorder (before PATH) requires a symaira-appkit
        // change; this is the best approximation within this repo.
        loc.extraDirectories = [Self.managedRuntimeDir] + loc.extraDirectories
        return loc
    }()

    private var detector: ToolDetector { ToolDetector(locator: locator) }
    /// Used only when `detector` finds nothing — see `CoreBinaryDiscovery`.
    private var relaxedDetector: ToolDetector { ToolDetector(locator: locator, allowUnverified: true) }

    @Published public private(set) var tool: DetectedTool?
    @Published public private(set) var isReady = false
    @Published public private(set) var errorMessage: String?

    /// Non-fatal note set when the core was found outside a strictly-trusted
    /// directory — most commonly a standard Homebrew prefix (#437). Nil when
    /// the strict provenance search succeeded.
    @Published public private(set) var coreProvenanceNote: String?

    /// The CLI version reported by `symdesk version --json`, cached once at
    /// connect and nil when the CLI is unreachable or remote.
    @Published public private(set) var coreVersion: String?

    @Published public var vaultPath: String?
	@Published public internal(set) var serverURL: URL?

    public var isDemoMode: Bool { VaultConfig.isDemoMode }
	public var isRemote: Bool { remoteClient != nil }
	internal var remoteClient: RemoteDeskClient?

	/// The active local-CLI or remote-HTTP transport, set alongside `tool`/
	/// `remoteClient` in `initialize()`. Feature methods route through this
	/// instead of branching on `remoteClient` individually.
	var transport: DeskTransport?

    private init() {}

    /// Appends `--vault <path>` when a vault is configured, empty otherwise.
    var vaultArgs: [String] {
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

        guard let detection = await CoreBinaryDiscovery.detect(
            deskTool,
            strict: detector,
            relaxed: relaxedDetector
        ) else {
            self.errorMessage = "symdesk binary not found. If Symbrain is installed, run 'symbrain setup'; otherwise install via Homebrew."
            return
        }
        let detected = detection.tool
        self.coreProvenanceNote = detection.provenanceNote

        do {
            try detector.requireSchemaVersion(1, of: detected)
            self.tool = detected
            self.transport = LocalDeskTransport(tool: detected)
            // Cache the CLI version for compatibility checks (issue #246).
            // A failure here is non-fatal — the version banner simply won't
            // appear, and every other screen still works.
            if let versionResult = try? await runCommandResult(arguments: ["version", "--json"]),
               let status = try? JSONDecoder().decode(DeskStatus.self, from: versionResult.stdout) {
                self.coreVersion = status.version
            }
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
		// Register the server as a peer entry in the vault registry so the
		// switcher can return to it after using a local vault (issue #296).
		// Reuses an existing entry for the same URL so reconnecting never
		// accumulates duplicate server entries.
		if let normalized = ServerConnectionConfig.normalizedURL(url) {
			let registry = VaultRegistry()
			let existing = registry.entries().first { entry in
				entry.kind == .server && entry.serverURL == normalized
			}
			let entry: VaultEntry
			if let existing {
				entry = existing
			} else {
				entry = VaultEntry.server(name: normalized.host ?? "Server", url: normalized)
				_ = registry.upsert(entry)
			}
			_ = registry.recordOpened(entry.id)
		}
	}

	public func disconnectServer() {
		ServerConnectionConfig.reset()
		remoteClient = nil
		transport = nil
		serverURL = nil
		isReady = false
	}

	func runChecked(arguments: [String], stdin: String = "") async throws -> Data {
		guard let transport else { throw DeskCoreError.coreNotFound }
		return try await transport.command(arguments: arguments, stdin: stdin)
	}

	private func runCommandResult(arguments: [String]) async throws -> CLIResult {
		guard let transport else { throw DeskCoreError.coreNotFound }
		return try await transport.commandResult(arguments: arguments)
	}

	func runDecoding<T: Decodable & Sendable>(_ type: T.Type, arguments: [String], stdin: String = "") async throws -> T {
		let data = try await runChecked(arguments: arguments, stdin: stdin)
		return try Self.decodeTolerantOfNullArray(type, from: data)
	}

	/// Decodes `data` as `T`, treating a top-level JSON `null` as an empty
	/// array when `T` is array-shaped.
	///
	/// Go's `encoding/json` marshals a nil slice as the literal `null`
	/// rather than `[]`. `Decodable`'s array container init cannot open an
	/// unkeyed container on `null`, so any CLI command that forgets to
	/// initialize its result slice crashes every array-typed decode on the
	/// empty case (e.g. a vault with zero meeting notes) with a raw
	/// "Cannot get unkeyed decoding container" error. This is defense in
	/// depth for that whole class of bug: the primary fix is on the Go
	/// side (return `[]T{}`, not a nil slice), but the Swift decode path
	/// stays tolerant so a future regression elsewhere degrades to an
	/// empty list instead of a crash.
	internal nonisolated static func decodeTolerantOfNullArray<T: Decodable & Sendable>(_ type: T.Type, from data: Data) throws -> T {
		do {
			return try JSONDecoder().decode(type, from: data)
		} catch {
			guard T.self is ExpressibleByArrayLiteral.Type,
				  let text = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines),
				  text == "null" else {
				throw error
			}
			return try JSONDecoder().decode(type, from: Data("[]".utf8))
		}
	}

    public func listFiles() async throws -> [Note] {
		try await runDecoding([Note].self, arguments: ["ls", "--json"] + vaultArgs)
    }

    public func getDoctorReport() async throws -> DoctorReport {
        // Use commandResult so stdout survives even when the CLI exits
        // non-zero (symdesk doctor --json exits 1 on warnings but writes
        // the complete report to stdout and nothing to stderr).
        let result = try await runCommandResult(arguments: ["doctor", "--json"] + vaultArgs)

        // Parse stdout regardless of exit code. Only fall back to an error
        // when stdout is not valid JSON — that means the CLI genuinely
        // could not produce a report.
        if let report = try? JSONDecoder().decode(DoctorReport.self, from: result.stdout) {
            return report
        }

        // No valid report on stdout: this is a genuine execution failure.
        throw DeskCoreError.cliExecutionFailed(
            exitCode: result.exitCode,
            stderr: result.stderrText
        )
    }

    public func getDoctor() async throws -> String {
		let report = try await getDoctorReport()
		if let json = try? JSONEncoder().encode(report), let str = String(data: json, encoding: .utf8) {
			return str
		}
		return "{\"overall\":\"\(report.overall)\"}"
    }

    public func search(query: String) async throws -> SearchResponse {
		try await runDecoding(SearchResponse.self, arguments: ["search", query, "--json"] + vaultArgs)
    }

    /// Reports the hybrid search index and its embedding backend (issue
    /// #515). Retrieval degrades silently — queries still answer, just worse
    /// — so this is the only way the app can tell the user why results got
    /// thin.
    public func retrievalStatus() async throws -> RetrievalStatus {
		try await runDecoding(RetrievalStatus.self, arguments: ["index", "status", "--json"] + vaultArgs)
    }

    /// Re-indexes the whole vault, optionally pruning entries for files that
    /// no longer exist. Mirrors `symdesk index [--prune]`.
    public func reindexVault(prune: Bool) async throws -> ReindexResult {
        var arguments = ["index", "--json"]
        if prune {
            arguments.append("--prune")
        }
		return try await runDecoding(ReindexResult.self, arguments: arguments + vaultArgs)
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

	/// Creates or opens today's daily note. Returns the note's vault-relative path.
	public func noteDaily() async throws -> String {
		struct NoteDailyResult: Codable, Sendable {
			let path: String
		}
		let res = try await runDecoding(NoteDailyResult.self, arguments: ["note", "daily", "--json"] + vaultArgs)
		return res.path
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

    public func baseList() async throws -> [DbBase] {
        try await runDecoding([DbBase].self, arguments: ["views", "base", "list", "--json"] + vaultArgs)
    }

    public func baseGet(ref: String) async throws -> DbBase {
        try await runDecoding(DbBase.self, arguments: ["views", "base", "get", ref, "--json"] + vaultArgs)
    }

    public func baseSave(_ base: DbBase) async throws {
        let encoder = JSONEncoder()
        let data = try encoder.encode(base)
        let json = String(decoding: data, as: UTF8.self)
        _ = try await runChecked(arguments: ["views", "base", "save", json, "--json"] + vaultArgs)
    }

    public func baseDelete(ref: String) async throws {
        _ = try await runChecked(arguments: ["views", "base", "delete", ref, "--json"] + vaultArgs)
    }

    public func viewsExportCSV(id: String) async throws -> Data {
        try await runChecked(arguments: ["views", "export-csv", id] + vaultArgs)
    }

    public func viewsExecuteEmbed(spec: String) async throws -> BaseEmbedResult {
        try await runDecoding(BaseEmbedResult.self, arguments: ["views", "embed", spec, "--json"] + vaultArgs)
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

    /// Lists a page of ingest jobs for the active vault.
    public func ingestJobPage(limit: Int = 100, offset: Int = 0) async throws -> IngestJobPage {
		guard let transport else { throw DeskCoreError.coreNotFound }
		return try await transport.ingestJobPage(vaultArgs: vaultArgs, limit: limit, offset: offset)
    }

	public func ingestRetry(jobID: String) async throws {
		guard let transport else { throw DeskCoreError.coreNotFound }
		try await transport.ingestRetry(jobID: jobID, vaultArgs: vaultArgs)
    }

    /// Streams an AI answer with optional work-surface context. A nil or
    /// empty context delegates to the original ask path unchanged, so scoped
    /// notebook callers continue to use `askScoped` without prompt rewriting.
    public func ask(query: String, context: DeskChatContext?, agent: Bool = false) -> AsyncThrowingStream<AIEvent, Error> {
        ask(query: context?.isEmpty == false ? context?.prompt(for: query) ?? query : query, agent: agent)
    }

    /// Streams an AI answer for the given query. When `agent` is true the
    /// bounded agentic tool loop runs instead of the one-shot ask: the CLI
    /// exposes only read-only tools and emits tool-call / tool-result events
    /// alongside the answer chunks (issue #317).
    public func ask(query: String, agent: Bool = false) -> AsyncThrowingStream<AIEvent, Error> {
        return AsyncThrowingStream { continuation in
            Task {
                guard let transport = self.transport else {
                    continuation.finish(throwing: DeskCoreError.coreNotFound)
                    return
                }
                do {
                    var args = ["ask", query, "--json"]
                    if agent { args.append("--agent") }
                    for try await line in transport.commandStream(arguments: args + self.vaultArgs, stdin: "") {
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

}

// MARK: - Meetings

extension DeskCore {
    /// Lists every meeting note already imported into the vault. Unlike
    /// `meetingsAvailable`, this always works even when `symmeet` is not on
    /// PATH, since it only walks already-written vault notes.
    public func meetingsList() async throws -> [MeetingNoteSummary] {
        try await runDecoding([MeetingNoteSummary].self, arguments: ["meeting", "list", "--json"] + vaultArgs)
    }

    /// Lists imported meeting notes and per-file decode failures. The latter
    /// are returned as data so the UI can offer a truthful reveal/skip path
    /// instead of silently dropping a malformed file.
    public func meetingsListReport() async throws -> MeetingListResult {
        try await runDecoding(MeetingListResult.self, arguments: ["meeting", "list", "--include-errors", "--json"] + vaultArgs)
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

/// One per-file failure returned by the detailed meeting-list endpoint.
/// Failures are non-fatal: the rest of the meeting library remains usable,
/// while the UI can name the file and offer reveal/skip actions.
public struct MeetingListFailure: Codable, Equatable, Identifiable, Sendable {
    public var id: String { path }
    public let path: String
    public let message: String

    public init(path: String, message: String) {
        self.path = path
        self.message = message
    }
}

/// Detailed meeting-list response used by the native UI. The legacy
/// `meetingsList()` array endpoint remains available for older callers.
public struct MeetingListResult: Codable, Equatable, Sendable {
    public let meetings: [MeetingNoteSummary]
    public let failures: [MeetingListFailure]

    public init(meetings: [MeetingNoteSummary], failures: [MeetingListFailure]) {
        self.meetings = meetings
        self.failures = failures
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
