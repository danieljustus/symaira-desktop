import Foundation

/// Mirrors the Go `notebook.Notebook` JSON shape (`internal/notebook`),
/// as returned by `notebook new|list|add-source|remove-source`.
public struct Notebook: Codable, Equatable, Identifiable, Sendable {
    public var id: String
    public let path: String
    public let title: String
    public let description: String?
    public let created: String
    public let sources: [String]

    enum CodingKeys: String, CodingKey {
        case id, path, title, description, created, sources
    }

    public init(id: String, path: String, title: String, description: String?, created: String, sources: [String]) {
        self.id = id
        self.path = path
        self.title = title
        self.description = description
        self.created = created
        self.sources = sources
    }

    // Custom decoding: tolerate `sources: null`. Go's own primary fix is
    // to never marshal a nil slice here (see internal/notebook.New /
    // .parse), but this is defense in depth for the same reason
    // DeskCore.decodeTolerantOfNullArray exists for top-level array
    // responses — a future Go-side regression should degrade to an empty
    // source list, not a decode failure with no indication which field.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        path = try c.decode(String.self, forKey: .path)
        title = try c.decode(String.self, forKey: .title)
        description = try c.decodeIfPresent(String.self, forKey: .description)
        created = try c.decode(String.self, forKey: .created)
        sources = try c.decodeIfPresent([String].self, forKey: .sources) ?? []
    }
}

/// One resolved source of a notebook, as returned by `notebook show`
/// (mirrors Go `notebook.SourceRef`). `missing` is true when the source
/// path no longer resolves to a file in the vault.
public struct NotebookSourceRef: Codable, Equatable, Identifiable, Sendable {
    public var id: String { path }
    public let path: String
    public let title: String?
    public let missing: Bool?

    public init(path: String, title: String?, missing: Bool?) {
        self.path = path
        self.title = title
        self.missing = missing
    }
}

/// The full detail shape returned by `notebook show --json`: a notebook
/// plus its sources resolved to their current titles.
public struct NotebookDetail: Codable, Equatable, Sendable {
    public let id: String
    public let path: String
    public let title: String
    public let description: String?
    public let created: String
    public let sources: [NotebookSourceRef]

    public init(id: String, path: String, title: String, description: String?, created: String, sources: [NotebookSourceRef]) {
        self.id = id
        self.path = path
        self.title = title
        self.description = description
        self.created = created
        self.sources = sources
    }
}

/// Mirrors the Go `service.NotebookGenerateResult` JSON shape, produced by
/// `notebook generate` (issue #426).
public struct NotebookArtifact: Codable, Equatable, Sendable {
    public let path: String
    public let kind: String
    public let content: String
    public let sources: [String]
    public let citationWarnings: [NotebookCitationWarning]?
    public let dryRun: Bool

    enum CodingKeys: String, CodingKey {
        case path, kind, content, sources
        case citationWarnings = "citation_warnings"
        case dryRun = "dry_run"
    }
}

/// Mirrors the Go `ai.CitationWarning` JSON shape.
public struct NotebookCitationWarning: Codable, Equatable, Sendable {
    public let path: String
    public let line: Int?
}

extension DeskCore {
    /// Creates a new notebook. Mirrors `symdesk notebook new`.
    public func notebookNew(title: String, description: String = "") async throws -> Notebook {
        var args = ["notebook", "new", title, "--json"]
        if !description.isEmpty {
            args.append(contentsOf: ["--description", description])
        }
        return try await runDecoding(Notebook.self, arguments: args + vaultArgs)
    }

    /// Lists every notebook in the vault. Mirrors `symdesk notebook list`.
    public func notebookList() async throws -> [Notebook] {
        try await runDecoding([Notebook].self, arguments: ["notebook", "list", "--json"] + vaultArgs)
    }

    /// Resolves one notebook and its current sources. Mirrors `symdesk
    /// notebook show`.
    public func notebookShow(_ id: String) async throws -> NotebookDetail {
        try await runDecoding(NotebookDetail.self, arguments: ["notebook", "show", id, "--json"] + vaultArgs)
    }

    /// Adds a vault file to a notebook's source set. Mirrors `symdesk
    /// notebook add-source`.
    public func notebookAddSource(_ id: String, path: String) async throws -> Notebook {
        try await runDecoding(Notebook.self, arguments: ["notebook", "add-source", id, path, "--json"] + vaultArgs)
    }

    /// Removes a vault file from a notebook's source set. The referenced
    /// file itself is never touched. Mirrors `symdesk notebook
    /// remove-source`.
    public func notebookRemoveSource(_ id: String, path: String) async throws -> Notebook {
        try await runDecoding(Notebook.self, arguments: ["notebook", "remove-source", id, path, "--json"] + vaultArgs)
    }

    /// Moves a notebook to the vault trash. Mirrors `symdesk notebook
    /// delete`.
    public func notebookDelete(_ id: String) async throws {
        _ = try await runChecked(arguments: ["notebook", "delete", id, "--json"] + vaultArgs)
    }

    /// Generates a studio artifact from a notebook's sources. Mirrors
    /// `symdesk notebook generate` (issue #426). `dryRun` computes and
    /// returns the artifact without writing it to the vault.
    public func notebookGenerate(_ id: String, kind: String, dryRun: Bool = false) async throws -> NotebookArtifact {
        var args = ["notebook", "generate", id, "--kind", kind, "--json"]
        if dryRun {
            args.append("--dry-run")
        }
        return try await runDecoding(NotebookArtifact.self, arguments: args + vaultArgs)
    }

    /// Streams an AI answer restricted to one notebook's sources: retrieval
    /// and citations never leave the notebook's scope (issue #425). Mirrors
    /// `symdesk ask --notebook <id>`.
    public func askScoped(query: String, notebook: String, agent: Bool = false) -> AsyncThrowingStream<AIEvent, Error> {
        return AsyncThrowingStream { continuation in
            Task {
                guard let transport = self.transport else {
                    continuation.finish(throwing: DeskCoreError.coreNotFound)
                    return
                }
                do {
                    var args = ["ask", query, "--notebook", notebook, "--json"]
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
}
