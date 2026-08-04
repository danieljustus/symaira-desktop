import Foundation

/// A ranked, persisted on-device search index over the parsed vault —
/// the plan's "equivalent inverted index" alternative to SQLite FTS5.
///
/// Design goals from #321:
/// - **Persisted**: the index survives cold starts, so search is usable
///   before the vault has been re-scanned. Bodies are never stored — only
///   normalized tokens per field — so memory stays bounded even for
///   multi-thousand-note vaults.
/// - **Incremental**: `merge(snapshot:)` re-indexes only notes whose
///   mtime+size signature changed since the last merge and drops notes
///   that disappeared, mirroring `MobileVaultScanner`'s cache logic.
/// - **Ranked**: field weight (title > tags > frontmatter > body) combined
///   with recency, so the right note is at the top, not parse order.
/// - **Prefix support**: typing `rech` finds `Rechnung` via a sorted
///   token array and binary-search prefix range.
/// - **Both connection modes feed the same path**: the store merges its
///   parsed snapshot (`[MobileNote]`) into this index regardless of whether
///   it came from the Files/iCloud scan or the server snapshot.
actor MobileSearchIndex {
    /// One indexed note. Tokens are deduplicated per field (a term
    /// occurring ten times still ranks the document once), which keeps the
    /// persisted index compact; ranking uses field weight + recency.
    struct Document: Codable, Sendable {
        let path: String
        /// "\(mtime)-\(size)" — the same signature the scanner cache uses.
        let signature: String
        let titleTokens: [String]
        let tagTokens: [String]
        let frontmatterTokens: [String]
        let bodyTokens: [String]
        let modifiedAt: Date
    }

    struct Result: Sendable, Equatable {
        let path: String
        let score: Double
    }

    private let fileURL: URL
    private var documents: [String: Document] = [:]
    /// token → vault-relative paths containing it.
    private var inverted: [String: [String]] = [:]
    /// Sorted unique tokens for binary-search prefix lookup.
    private var sortedTokens: [String] = []

    private let titleWeight = 6.0
    private let tagWeight = 4.0
    private let frontmatterWeight = 3.0
    private let bodyWeight = 1.0
    /// Multiplier when every query token matched (AND bonus).
    private let fullMatchBonus = 1.5
    /// Decay half-life for recency scoring (days).
    private let recencyHalfLifeDays = 30.0

    private struct Persisted: Codable {
        let documents: [Document]
    }

    init(fileURL: URL) {
        self.fileURL = fileURL
        let docs = Self.loadDocuments(from: fileURL)
        self.documents = docs
        let built = Self.buildInverted(docs)
        self.inverted = built.inverted
        self.sortedTokens = built.sorted
    }

    // MARK: - Indexing

    /// Incrementally re-indexes the given snapshot. Notes whose signature
    /// is unchanged keep their index entries; changed notes are re-indexed;
    /// notes absent from the snapshot are removed.
    func merge(snapshot: [MobileNote]) {
        var nextDocuments: [String: Document] = [:]
        nextDocuments.reserveCapacity(snapshot.count)

        for note in snapshot {
            let signature = Self.signature(for: note)
            if let existing = documents[note.path], existing.signature == signature {
                nextDocuments[note.path] = existing
                continue
            }
            nextDocuments[note.path] = index(note)
        }

        documents = nextDocuments
        rebuildInverted()
        persist()
    }

    func removeAll() {
        documents.removeAll()
        inverted.removeAll()
        sortedTokens.removeAll()
        try? FileManager.default.removeItem(at: fileURL)
    }

    /// The number of indexed notes (for diagnostics/tests).
    var documentCount: Int { documents.count }

    // MARK: - Query

    /// Ranked prefix search. `query` is normalized the same way the parser
    /// normalizes note text, so `Überfällig` and `uberfallig` match.
    func search(query: String, limit: Int = 50) -> [Result] {
        let tokens = Self.tokenize(query)
        guard !tokens.isEmpty else { return [] }
        guard !documents.isEmpty else { return [] }

        // token → docs that contain a token with this prefix (or exactly).
        // AND semantics: a document must match every query token, so
        // "Rechnung Juli" behaves like the old substring contains and
        // stays consistent with the filter semantics (#322).
        var perTokenPaths: [[String]] = []
        var matchedPaths: Set<String>? = nil
        for token in tokens {
            let prefixPaths = paths(forPrefix: token)
            if prefixPaths.isEmpty { return [] }  // every token must match something
            perTokenPaths.append(prefixPaths)
            let pathSet = Set(prefixPaths)
            if matchedPaths == nil {
                matchedPaths = pathSet
            } else {
                matchedPaths?.formIntersection(pathSet)
            }
        }
        guard let paths = matchedPaths, !paths.isEmpty else { return [] }

        var results: [Result] = []
        results.reserveCapacity(paths.count)

        for path in paths {
            guard let doc = documents[path] else { continue }
            var score = 0.0
            for (index, token) in tokens.enumerated() {
                let exact = containsExact(doc, token: token)
                let prefix = perTokenPaths[index].contains(path)
                guard exact || prefix else { continue }
                let fieldWeight = bestFieldWeight(doc, token: token)
                let matchFactor = exact ? 1.0 : 0.6
                score += fieldWeight * matchFactor
            }
            if tokens.count > 1 { score *= fullMatchBonus }
            score += recencyScore(for: doc.modifiedAt)
            results.append(Result(path: path, score: score))
        }

        results.sort { lhs, rhs in
            if lhs.score != rhs.score { return lhs.score > rhs.score }
            let lDate = documents[lhs.path]?.modifiedAt ?? .distantPast
            let rDate = documents[rhs.path]?.modifiedAt ?? .distantPast
            return lDate > rDate
        }
        return Array(results.prefix(limit))
    }

    // MARK: - Tokenization (shared with the parser's normalization)

    /// Splits text into normalized tokens: diacritic-folded, lowercased,
    /// non-alphanumeric separators. Two or more characters, so single
    /// letters do not pollute the index.
    static func tokenize(_ text: String) -> [String] {
        let folded = text
            .folding(options: [.caseInsensitive, .diacriticInsensitive], locale: .current)
            .lowercased()
        let scalars = folded.unicodeScalars.map { Character($0) }
        var tokens: [String] = []
        var current = ""
        for character in scalars {
            if character.isLetter || character.isNumber {
                current.append(character)
            } else if !current.isEmpty {
                if current.count >= 2 { tokens.append(current) }
                current = ""
            }
        }
        if current.count >= 2 { tokens.append(current) }
        return tokens
    }

    static func signature(for note: MobileNote) -> String {
        "\(Int(note.modifiedAt.timeIntervalSince1970))-\(note.fileSize)"
    }

    // MARK: - Private

    private func index(_ note: MobileNote) -> Document {
        Document(
            path: note.path,
            signature: Self.signature(for: note),
            titleTokens: Self.tokenize(note.title),
            tagTokens: Self.tokenize(note.tags.joined(separator: " ")),
            frontmatterTokens: Self.tokenize(
                [note.correspondent, note.documentType, note.person, note.status, note.dueDate]
                    .joined(separator: " ")
            ),
            bodyTokens: Self.tokenize(note.body),
            modifiedAt: note.modifiedAt
        )
    }

    private func rebuildInverted() {
        var newInverted: [String: [String]] = [:]
        for (path, doc) in documents {
            for token in Set(doc.titleTokens + doc.tagTokens + doc.frontmatterTokens + doc.bodyTokens) {
                newInverted[token, default: []].append(path)
            }
        }
        inverted = newInverted
        sortedTokens = inverted.keys.sorted()
    }

    /// Paths of documents containing any token with the given prefix.
    private func paths(forPrefix prefix: String) -> [String] {
        var paths: Set<String> = []
        for token in tokens(withPrefix: prefix) {
            if let tokenPaths = inverted[token] {
                paths.formUnion(tokenPaths)
            }
        }
        return Array(paths)
    }

    /// Binary-search range of tokens starting with `prefix`.
    private func tokens(withPrefix prefix: String) -> [String] {
        var lower = 0
        var upper = sortedTokens.count
        while lower < upper {
            let mid = (lower + upper) / 2
            if sortedTokens[mid] < prefix {
                lower = mid + 1
            } else {
                upper = mid
            }
        }
        var result: [String] = []
        var index = lower
        while index < sortedTokens.count, sortedTokens[index].hasPrefix(prefix) {
            result.append(sortedTokens[index])
            index += 1
        }
        return result
    }

    private func containsExact(_ doc: Document, token: String) -> Bool {
        doc.titleTokens.contains(token)
            || doc.tagTokens.contains(token)
            || doc.frontmatterTokens.contains(token)
            || doc.bodyTokens.contains(token)
    }

    private func bestFieldWeight(_ doc: Document, token: String) -> Double {
        if doc.titleTokens.contains(token) { return titleWeight }
        if doc.tagTokens.contains(token) { return tagWeight }
        if doc.frontmatterTokens.contains(token) { return frontmatterWeight }
        return bodyWeight
    }

    private func recencyScore(for date: Date) -> Double {
        let days = max(0, Date().timeIntervalSince(date) / 86_400)
        return pow(0.5, days / recencyHalfLifeDays) * 1.5
    }

    // MARK: - Persistence

    private static func loadDocuments(from fileURL: URL) -> [String: Document] {
        guard let data = try? Data(contentsOf: fileURL),
              let persisted = try? JSONDecoder().decode(Persisted.self, from: data) else { return [:] }
        return Dictionary(uniqueKeysWithValues: persisted.documents.map { ($0.path, $0) })
    }

    private static func buildInverted(_ documents: [String: Document]) -> (inverted: [String: [String]], sorted: [String]) {
        var newInverted: [String: [String]] = [:]
        for (path, doc) in documents {
            for token in Set(doc.titleTokens + doc.tagTokens + doc.frontmatterTokens + doc.bodyTokens) {
                newInverted[token, default: []].append(path)
            }
        }
        return (newInverted, newInverted.keys.sorted())
    }

    private func persist() {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        guard let data = try? encoder.encode(Persisted(documents: Array(documents.values))) else { return }
        try? FileManager.default.createDirectory(
            at: fileURL.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        try? data.write(to: fileURL, options: .atomic)
    }
}

/// Snippet generation for search results: a context window around the
/// first match of the query in the body, falling back to the first line.
enum MobileSearchSnippet {
    static func snippet(for body: String, normalizedQuery: String, radius: Int = 70) -> String {
        let foldedBody = body.folding(options: [.caseInsensitive, .diacriticInsensitive], locale: .current)
            .lowercased()
        let foldedQuery = normalizedQuery
            .folding(options: [.caseInsensitive, .diacriticInsensitive], locale: .current)
            .lowercased()
            .trimmingCharacters(in: .whitespacesAndNewlines)

        guard !foldedQuery.isEmpty else {
            return firstLine(of: body)
        }
        guard let range = foldedBody.range(of: foldedQuery) else {
            return firstLine(of: body)
        }

        let start = body.index(range.lowerBound, offsetBy: -radius, limitedBy: body.startIndex) ?? body.startIndex
        let end = body.index(range.upperBound, offsetBy: radius, limitedBy: body.endIndex) ?? body.endIndex
        var snippet = String(body[start..<end])
            .replacingOccurrences(of: "\n", with: " ")
        if start > body.startIndex { snippet = "…" + snippet }
        if end < body.endIndex { snippet += "…" }
        return snippet
    }

    private static func firstLine(of body: String) -> String {
        let line = body.split(separator: "\n", maxSplits: 1, omittingEmptySubsequences: false).first.map(String.init) ?? ""
        return line.count > 140 ? String(line.prefix(140)) + "…" : line
    }
}
