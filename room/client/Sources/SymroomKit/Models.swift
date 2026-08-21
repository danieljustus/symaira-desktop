import Foundation

/// Member as returned by `symroom member list --json`.
public struct RoomMember: Codable, Identifiable, Sendable, Equatable {
    public let id: String
    public let name: String
    public let role: String
    public let kind: String
}

/// A run as returned by `symroom run list --json` / `run show --json`.
public struct Run: Codable, Identifiable, Sendable, Equatable {
    public let id: String
    public let title: String
    public let planFile: String?
    public let adapter: String?
    public let state: String
    public let author: String
    public let createdAt: String
    public let updatedAt: String
    public let approvalID: String?
    public let scope: String?
    public let expiresAt: String?
    public let summary: String?
    public let error: String?
    public let artifacts: [String]?
    public let checkpoints: [Checkpoint]?

    public var isPending: Bool { state == "requested" }
}

/// A checkpoint within a run (`symroom checkpoint list` / run detail).
public struct Checkpoint: Codable, Identifiable, Sendable, Equatable {
    public let id: String
    public let runID: String?
    public let question: String
    public let answer: String?
    public let state: String
    public let author: String
    public let createdAt: String
    public let updatedAt: String?
}

/// Minimal JSON value type for the journal event `body` payload — the room
/// events carry heterogeneous JSON objects (`note.posted`, `run.requested`,
/// …), so the module keeps them as a typed tree instead of reimplementing
/// per-kind schemas.
public enum JSONValue: Codable, Sendable, Equatable {
    case string(String)
    case number(Double)
    case bool(Bool)
    case object([String: JSONValue])
    case array([JSONValue])
    case null

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if let s = try? c.decode(String.self) { self = .string(s); return }
        if let n = try? c.decode(Double.self) { self = .number(n); return }
        if let b = try? c.decode(Bool.self) { self = .bool(b); return }
        if let arr = try? c.decode([JSONValue].self) { self = .array(arr); return }
        if let obj = try? c.decode([String: JSONValue].self) { self = .object(obj); return }
        if c.decodeNil() { self = .null; return }
        throw DecodingError.dataCorruptedError(in: c, debugDescription: "unsupported JSON value")
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .string(let s): try c.encode(s)
        case .number(let n): try c.encode(n)
        case .bool(let b): try c.encode(b)
        case .object(let o): try c.encode(o)
        case .array(let a): try c.encode(a)
        case .null: try c.encodeNil()
        }
    }

    /// Compact one-line representation for display and filtering.
    public var displayString: String {
        switch self {
        case .string(let s): return s
        case .number(let n): return String(n)
        case .bool(let b): return String(b)
        case .null: return "null"
        case .array(let a): return a.map(\.displayString).joined(separator: ", ")
        case .object(let o):
            return o.map { "\($0.key): \($0.value.displayString)" }
                .sorted()
                .joined(separator: ", ")
        }
    }
}

/// A journal event as returned by `symroom log --json` (NDJSON lines).
public struct JournalEvent: Decodable, Identifiable, Sendable, Equatable {
    public let v: Int?
    public let id: String
    public let room: String?
    public let author: String
    public let seq: UInt64?
    public let prev: String?
    public let lamport: UInt64?
    public let ts: String
    public let kind: String
    public let body: JSONValue?

    private enum CodingKeys: String, CodingKey {
        case v, id, room, author, seq, prev, lamport, ts, kind, body
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        v = try c.decodeIfPresent(Int.self, forKey: .v)
        id = try c.decode(String.self, forKey: .id)
        room = try c.decodeIfPresent(String.self, forKey: .room)
        author = try c.decode(String.self, forKey: .author)
        seq = try c.decodeIfPresent(UInt64.self, forKey: .seq)
        prev = try c.decodeIfPresent(String.self, forKey: .prev)
        lamport = try c.decodeIfPresent(UInt64.self, forKey: .lamport)
        ts = try c.decode(String.self, forKey: .ts)
        kind = try c.decode(String.self, forKey: .kind)
        body = try c.decodeIfPresent(JSONValue.self, forKey: .body)
    }
}

/// The decoded view model for the module: members, journal, pending runs.
public struct RoomSnapshot: Sendable, Equatable {
    public let members: [RoomMember]
    public let journal: [JournalEvent]
    public let pendingRuns: [Run]

    public init(members: [RoomMember], journal: [JournalEvent], pendingRuns: [Run]) {
        self.members = members
        self.journal = journal
        self.pendingRuns = pendingRuns
    }
}
