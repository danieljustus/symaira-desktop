import Foundation

// MARK: - History & Trash Models

public struct HistoryEntry: Codable, Equatable, Identifiable, Sendable {
    public var id: String { snapshotID }
    public let snapshotID: String
    public let timestamp: String
    public let size: Int64

    public init(snapshotID: String, timestamp: String, size: Int64) {
        self.snapshotID = snapshotID
        self.timestamp = timestamp
        self.size = size
    }

    enum CodingKeys: String, CodingKey {
        case snapshotID = "id"
        case timestamp, size
    }
}

public struct TrashEntry: Codable, Equatable, Identifiable, Sendable {
    public var id: String { name }
    public let name: String
    public let originalPath: String
    public let deletedAt: String
    public let size: Int64

    public init(name: String, originalPath: String, deletedAt: String, size: Int64) {
        self.name = name
        self.originalPath = originalPath
        self.deletedAt = deletedAt
        self.size = size
    }

    enum CodingKeys: String, CodingKey {
        case name
        case originalPath = "original_path"
        case deletedAt = "deleted_at"
        case size
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

// MARK: - Doctor Report

public struct DoctorReport: Codable, Sendable {
    public let overall: String
    public let vault: SubsystemStatus?
    public let sidecar: SubsystemStatus?
    public let contract: SubsystemStatus?
    public let tools: ToolAvailability
    public let versions: [String: String]?
    public let conflicts: [String]?
    public let ai: AIReport?

    public struct SubsystemStatus: Codable, Sendable {
        public let status: String?
        public let message: String?
        public let path: String?
        public let filesFound: Int?

        enum CodingKeys: String, CodingKey {
            case status
            case message
            case path
            case filesFound = "files_found"
        }

        public init(status: String? = nil, message: String? = nil, path: String? = nil, filesFound: Int? = nil) {
            self.status = status
            self.message = message
            self.path = path
            self.filesFound = filesFound
        }
    }

    public struct AIReport: Codable, Sendable {
        public let provider: String?
        public let model: String?

        public init(provider: String? = nil, model: String? = nil) {
            self.provider = provider
            self.model = model
        }
    }

    /// The sibling binaries `symdesk doctor` still probes. Search, PDF
    /// rendering, contacts, meeting capture and document ingest were absorbed
    /// into `symdesk` by the repo consolidation and are therefore no longer
    /// reported here.
    public struct ToolAvailability: Codable, Sendable {
        /// Metadata for the separate companion tools reported by `symdesk doctor`.
        /// Keep the identifiers aligned with the JSON payload keys below.
        public struct ManagedTool: Equatable, Sendable {
            public let id: String
            public let name: String
            public let tap: String

            public init(id: String, name: String, tap: String) {
                self.id = id
                self.name = name
                self.tap = tap
            }
        }

        public static let managedTools: [ManagedTool] = [
            ManagedTool(id: "symmemory", name: "SymMemory", tap: "danieljustus/tap/symmemory"),
            ManagedTool(id: "symvault", name: "SymVault", tap: "danieljustus/tap/symvault"),
            ManagedTool(id: "symbrowse", name: "SymBrowse", tap: "danieljustus/tap/symbrowse"),
        ]

        public let symmemory: String?
        public let symvault: String?
        public let symbrowse: String?

        public init(symmemory: String? = nil, symvault: String? = nil, symbrowse: String? = nil) {
            self.symmemory = symmemory
            self.symvault = symvault
            self.symbrowse = symbrowse
        }

        /// Whether a tool name resolves to "ok" or "available".
        public func isAvailable(_ name: String) -> Bool {
            let val: String?
            switch name {
            case "symmemory": val = symmemory
            case "symvault": val = symvault
            case "symbrowse": val = symbrowse
            default: val = nil
            }
            guard let v = val else { return false }
            let lower = v.lowercased()
            return lower == "ok" || lower == "available" || lower == "found"
        }
    }

    public init(
        overall: String = "unknown",
        vault: SubsystemStatus? = nil,
        sidecar: SubsystemStatus? = nil,
        contract: SubsystemStatus? = nil,
        tools: ToolAvailability = ToolAvailability(),
        versions: [String: String]? = nil,
        conflicts: [String]? = nil,
        ai: AIReport? = nil
    ) {
        self.overall = overall
        self.vault = vault
        self.sidecar = sidecar
        self.contract = contract
        self.tools = tools
        self.versions = versions
        self.conflicts = conflicts
        self.ai = ai
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        overall = (try? c.decode(String.self, forKey: .overall)) ?? "unknown"
        vault = try? c.decode(SubsystemStatus.self, forKey: .vault)
        sidecar = try? c.decode(SubsystemStatus.self, forKey: .sidecar)
        contract = try? c.decode(SubsystemStatus.self, forKey: .contract)
        tools = (try? c.decode(ToolAvailability.self, forKey: .tools)) ?? ToolAvailability()
        versions = try? c.decode([String: String].self, forKey: .versions)
        conflicts = try? c.decode([String].self, forKey: .conflicts)
        ai = try? c.decode(AIReport.self, forKey: .ai)
    }
}
