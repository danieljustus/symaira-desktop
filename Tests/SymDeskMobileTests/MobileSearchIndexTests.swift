import Foundation
import XCTest
@testable import SymDeskMobile

/// Tests for the ranked on-device search index (#321): ranking by field
/// weight, prefix matching, snippet generation, incremental invalidation
/// against the mtime/size signature and persistence across recreation.
final class MobileSearchIndexTests: XCTestCase {

    private var indexURL: URL!

    override func setUpWithError() throws {
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("SearchIndexTests-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        indexURL = dir.appendingPathComponent("index.json")
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: indexURL.deletingLastPathComponent())
    }

    // MARK: - Fixtures

    private func makeIndex() -> MobileSearchIndex {
        MobileSearchIndex(fileURL: indexURL)
    }

    private func note(
        _ name: String,
        title: String,
        body: String,
        tags: [String] = [],
        correspondent: String = "",
        documentType: String = "",
        modifiedAt: Date = Date(timeIntervalSince1970: 1_000)
    ) throws -> MobileNote {
        let root = URL(fileURLWithPath: "/tmp/SymDeskMobileVault", isDirectory: true)
        let source = """
        ---
        title: "\(title)"
        tags: [\(tags.map { "\"\($0)\"" }.joined(separator: ", "))]
        correspondent: "\(correspondent)"
        document_type: "\(documentType)"
        ---

        \(body)
        """
        return try MobileVaultParser.parse(
            data: Data(source.utf8),
            fileURL: root.appendingPathComponent("\(name).md"),
            root: root,
            modifiedAt: modifiedAt
        )
    }

    // MARK: - Ranking

    func testTitleMatchOutranksBodyMatch() async throws {
        let index = makeIndex()
        // Same term, same recency: one hits in the title, one only in the body.
        let titleHit = try note("title-hit", title: "Rechnung Juli", body: "irrelevant text", modifiedAt: Date(timeIntervalSince1970: 2_000))
        let bodyHit = try note("body-hit", title: "Notizen", body: "Rechnung Juli und mehr text", modifiedAt: Date(timeIntervalSince1970: 2_000))
        await index.merge(snapshot: [titleHit, bodyHit])

        let results = await index.search(query: "Rechnung")
        XCTAssertEqual(results.first?.path, "title-hit.md", "title match must outrank body match")
    }

    func testTagMatchOutranksBodyMatch() async throws {
        let index = makeIndex()
        let tagHit = try note("tag-hit", title: "Notizen", body: "irrelevant", tags: ["finance"], modifiedAt: Date(timeIntervalSince1970: 2_000))
        let bodyHit = try note("body-hit", title: "Notizen", body: "finance document text", modifiedAt: Date(timeIntervalSince1970: 2_000))
        await index.merge(snapshot: [tagHit, bodyHit])

        let results = await index.search(query: "finance")
        XCTAssertEqual(results.first?.path, "tag-hit.md", "tag match must outrank body match")
    }

    func testRecencyBreaksTies() async throws {
        let index = makeIndex()
        let old = try note("old", title: "Rechnung", body: "x", modifiedAt: Date(timeIntervalSince1970: 100))
        let recent = try note("recent", title: "Rechnung", body: "x", modifiedAt: Date(timeIntervalSince1970: 9_999_999))
        await index.merge(snapshot: [old, recent])

        let results = await index.search(query: "Rechnung")
        XCTAssertEqual(results.first?.path, "recent.md", "more recent note must rank first on a tie")
    }

    // MARK: - Prefix matching

    func testPrefixQueryFindsExpandedToken() async throws {
        let index = makeIndex()
        let note = try note("rechnung", title: "Rechnung", body: "Text")
        await index.merge(snapshot: [note])

        let results = await index.search(query: "rech")
        XCTAssertEqual(results.first?.path, "rechnung.md", "typing rech must find Rechnung")
    }

    func testMultiTokenQueryRequiresAllTokens() async throws {
        let index = makeIndex()
        let both = try note("both", title: "Rechnung Juli", body: "Text")
        let onlyOne = try note("one", title: "Rechnung", body: "kein anderer Inhalt hier")
        await index.merge(snapshot: [both, onlyOne])

        let results = await index.search(query: "Rechnung Juli")
        XCTAssertEqual(results.map(\.path), ["both.md"], "only the note matching every token should surface")
    }

    // MARK: - Snippets

    func testSnippetShowsMatchContext() {
        let body = "Erste Zeile mit viel Kontext davor und weiteren Worten, damit der Treffer in der Mitte liegt. Die Rechnung vom 3. Juli liegt hier. Danach folgt noch eine lange Zeile mit weiterem Text, der den Radius auf der anderen Seite füllt und abschließt."
        let snippet = MobileSearchSnippet.snippet(for: body, normalizedQuery: "rechnung")
        XCTAssertTrue(snippet.contains("Rechnung"), "snippet must contain the matched term: \(snippet)")
        XCTAssertTrue(snippet.hasPrefix("…"), "snippet should be truncated on the left")
        XCTAssertTrue(snippet.hasSuffix("…"), "snippet should be truncated on the right")
    }

    func testSnippetFallsBackToFirstLineWhenNoMatch() {
        let body = "Erste Zeile.\n\nGanz anderer Inhalt."
        let snippet = MobileSearchSnippet.snippet(for: body, normalizedQuery: "unbekannt")
        XCTAssertEqual(snippet, "Erste Zeile.")
    }

    func testSnippetHandlesEmptyQuery() {
        XCTAssertEqual(MobileSearchSnippet.snippet(for: "Hello world", normalizedQuery: ""), "Hello world")
    }

    // MARK: - Incremental invalidation

    func testMergeReindexesOnlyChangedNotes() async throws {
        let index = makeIndex()
        let a = try note("a", title: "Alpha", body: "alpha body", modifiedAt: Date(timeIntervalSince1970: 100))
        let b = try note("b", title: "Beta", body: "beta body", modifiedAt: Date(timeIntervalSince1970: 200))
        await index.merge(snapshot: [a, b])
        let docCount = await index.documentCount
        XCTAssertEqual(docCount, 2)

        // Same signatures → merge must be a no-op (no re-index, no removal).
        await index.merge(snapshot: [a, b])
        let unchangedCount = await index.documentCount
        XCTAssertEqual(unchangedCount, 2)

        // b changed on disk (new mtime) → only b is re-indexed.
        let b2 = try note("b", title: "Beta 2", body: "beta body 2", modifiedAt: Date(timeIntervalSince1970: 300))
        await index.merge(snapshot: [a, b2])
        let reindexedCount = await index.documentCount
        XCTAssertEqual(reindexedCount, 2)
        let results = await index.search(query: "Beta 2")
        XCTAssertEqual(results.first?.path, "b.md")
        let stale = await index.search(query: "Alpha")
        XCTAssertEqual(stale.first?.path, "a.md", "unchanged note must keep its index entry")
    }

    func testMergeDropsRemovedNotes() async throws {
        let index = makeIndex()
        let a = try note("a", title: "Alpha", body: "x")
        let b = try note("b", title: "Beta", body: "x")
        await index.merge(snapshot: [a, b])

        // a disappeared from the vault.
        await index.merge(snapshot: [b])
        let docCount = await index.documentCount
        XCTAssertEqual(docCount, 1)
        let results = await index.search(query: "Alpha")
        XCTAssertTrue(results.isEmpty, "removed note must vanish from the index")
    }

    func testSignatureChangesWithContentSize() throws {
        let root = URL(fileURLWithPath: "/tmp/SymDeskMobileVault", isDirectory: true)
        let fileURL = root.appendingPathComponent("n.md")
        let small = try MobileVaultParser.parse(
            data: Data("short".utf8), fileURL: fileURL, root: root, modifiedAt: Date(timeIntervalSince1970: 1)
        )
        let large = try MobileVaultParser.parse(
            data: Data("a much longer body of text".utf8), fileURL: fileURL, root: root, modifiedAt: Date(timeIntervalSince1970: 1)
        )
        XCTAssertNotEqual(
            MobileSearchIndex.signature(for: small),
            MobileSearchIndex.signature(for: large),
            "size change must invalidate the index entry even at the same mtime"
        )
    }

    // MARK: - Persistence

    func testIndexSurvivesRecreation() async throws {
        let index = makeIndex()
        let a = try note("a", title: "Alpha", body: "body")
        let b = try note("b", title: "Beta", body: "body")
        await index.merge(snapshot: [a, b])

        // Simulate cold start: a fresh index over the same file.
        let reloaded = MobileSearchIndex(fileURL: indexURL)
        let reloadedCount = await reloaded.documentCount
        XCTAssertEqual(reloadedCount, 2)
        let results = await reloaded.search(query: "Alpha")
        XCTAssertEqual(results.first?.path, "a.md", "cold start must serve search from the persisted index")
    }

    func testRemoveAllPurgesIndex() async throws {
        let index = makeIndex()
        let a = try note("a", title: "Alpha", body: "body")
        await index.merge(snapshot: [a])
        await index.removeAll()

        let docCount = await index.documentCount
        XCTAssertEqual(docCount, 0)
        let reloaded = MobileSearchIndex(fileURL: indexURL)
        let reloadedCount = await reloaded.documentCount
        XCTAssertEqual(reloadedCount, 0, "purge must survive recreation (file removed)")
    }

    // MARK: - Tokenization

    func testTokenizeNormalizesDiacriticsAndSplits() {
        XCTAssertEqual(MobileSearchIndex.tokenize("Überfällige Rechnung!"), ["uberfallige", "rechnung"])
        XCTAssertEqual(MobileSearchIndex.tokenize("invoice-2026/07"), ["invoice", "2026", "07"])
        XCTAssertEqual(MobileSearchIndex.tokenize("a b"), [])
    }
}
