import XCTest
@testable import SymDeskCore

final class DeskCoreNotebooksTests: XCTestCase {
    func testNotebookDecoding() throws {
        let json = """
        {
            "id": "research-x",
            "path": "notebooks/research-x.md",
            "title": "Research X",
            "description": "notes on X",
            "created": "2026-08-10T10:00:00Z",
            "sources": ["docs/a.md", "docs/b.md"]
        }
        """.data(using: .utf8)!

        let notebook = try JSONDecoder().decode(Notebook.self, from: json)
        XCTAssertEqual(notebook.id, "research-x")
        XCTAssertEqual(notebook.path, "notebooks/research-x.md")
        XCTAssertEqual(notebook.title, "Research X")
        XCTAssertEqual(notebook.description, "notes on X")
        XCTAssertEqual(notebook.sources, ["docs/a.md", "docs/b.md"])
    }

    /// Go's own contract fix (internal/notebook.New/.parse, issue #427)
    /// ensures `sources` is never marshaled as `null`. This test guards
    /// the Swift-side defense in depth for that same class of bug: a nil
    /// slice decoded into a non-optional array crashes the whole decode
    /// with a generic "data couldn't be read" error that gives no
    /// indication which field caused it. See DeskCore.decodeTolerantOfNullArray
    /// for the equivalent top-level-response tolerance.
    func testNotebookDecodingToleratesNullSources() throws {
        let json = """
        {
            "id": "empty-nb",
            "path": "notebooks/empty-nb.md",
            "title": "Empty NB",
            "created": "2026-08-10T10:00:00Z",
            "sources": null
        }
        """.data(using: .utf8)!

        let notebook = try JSONDecoder().decode(Notebook.self, from: json)
        XCTAssertEqual(notebook.sources, [])
        XCTAssertNil(notebook.description)
    }

    func testNotebookListDecodingToleratesTopLevelNull() throws {
        // Mirrors the DeskTransport response shape a CLI/server "null"
        // result would produce for a vault with zero notebooks.
        let data = "null".data(using: .utf8)!
        let list = try DeskCore.decodeTolerantOfNullArray([Notebook].self, from: data)
        XCTAssertEqual(list, [])
    }

    func testNotebookSourceRefDecoding() throws {
        let json = """
        {"path": "docs/present.md", "title": "Present Doc"}
        """.data(using: .utf8)!
        let ref = try JSONDecoder().decode(NotebookSourceRef.self, from: json)
        XCTAssertEqual(ref.path, "docs/present.md")
        XCTAssertEqual(ref.title, "Present Doc")
        XCTAssertNil(ref.missing)
    }

    func testNotebookSourceRefDecodingMissingSource() throws {
        let json = """
        {"path": "docs/gone.md", "missing": true}
        """.data(using: .utf8)!
        let ref = try JSONDecoder().decode(NotebookSourceRef.self, from: json)
        XCTAssertEqual(ref.path, "docs/gone.md")
        XCTAssertNil(ref.title)
        XCTAssertEqual(ref.missing, true)
    }

    func testNotebookDetailDecoding() throws {
        let json = """
        {
            "id": "research-x",
            "path": "notebooks/research-x.md",
            "title": "Research X",
            "description": "",
            "created": "2026-08-10T10:00:00Z",
            "sources": [
                {"path": "docs/a.md", "title": "A"},
                {"path": "docs/gone.md", "missing": true}
            ]
        }
        """.data(using: .utf8)!

        let detail = try JSONDecoder().decode(NotebookDetail.self, from: json)
        XCTAssertEqual(detail.sources.count, 2)
        XCTAssertEqual(detail.sources[0].title, "A")
        XCTAssertEqual(detail.sources[1].missing, true)
    }

    func testNotebookArtifactDecoding() throws {
        let json = """
        {
            "path": "notebooks/research-x/briefing.md",
            "kind": "briefing",
            "content": "Generated briefing text.",
            "sources": ["docs/a.md"],
            "citation_warnings": [{"path": "docs/unread.md"}],
            "dry_run": false
        }
        """.data(using: .utf8)!

        let artifact = try JSONDecoder().decode(NotebookArtifact.self, from: json)
        XCTAssertEqual(artifact.kind, "briefing")
        XCTAssertEqual(artifact.content, "Generated briefing text.")
        XCTAssertEqual(artifact.sources, ["docs/a.md"])
        XCTAssertEqual(artifact.citationWarnings?.count, 1)
        XCTAssertEqual(artifact.citationWarnings?.first?.path, "docs/unread.md")
        XCTAssertFalse(artifact.dryRun)
    }

    func testNotebookArtifactDecodingWithoutCitationWarnings() throws {
        let json = """
        {
            "path": "notebooks/research-x/faq.md",
            "kind": "faq",
            "content": "Generated FAQ.",
            "sources": [],
            "dry_run": true
        }
        """.data(using: .utf8)!

        let artifact = try JSONDecoder().decode(NotebookArtifact.self, from: json)
        XCTAssertNil(artifact.citationWarnings)
        XCTAssertTrue(artifact.dryRun)
    }
}
