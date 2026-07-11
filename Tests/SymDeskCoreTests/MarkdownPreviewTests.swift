import XCTest
@testable import SymDeskCore

final class MarkdownPreviewParserTests: XCTestCase {

    func testParsesCallout() {
        let md = """
        > [!warning] Watch out
        > This is dangerous.
        > Second line.
        """
        let blocks = MarkdownPreviewParser.parse(md)
        XCTAssertEqual(blocks, [.callout(type: "warning", title: "Watch out", lines: ["This is dangerous.", "Second line."])])
    }

    func testCalloutWithoutTitleUsesTypeName() {
        let blocks = MarkdownPreviewParser.parse("> [!note]\n> Body")
        XCTAssertEqual(blocks, [.callout(type: "note", title: "Note", lines: ["Body"])])
    }

    func testFoldableCalloutMarkerIsStripped() {
        let blocks = MarkdownPreviewParser.parse("> [!tip]- Folded title\n> Body")
        XCTAssertEqual(blocks, [.callout(type: "tip", title: "Folded title", lines: ["Body"])])
    }

    func testPlainQuoteIsNotCallout() {
        let blocks = MarkdownPreviewParser.parse("> just a quote")
        XCTAssertEqual(blocks, [.quote(lines: ["just a quote"])])
    }

    func testParsesEmbed() {
        XCTAssertEqual(MarkdownPreviewParser.parseEmbed("![[Other Note]]"), .embed(target: "Other Note", heading: nil))
        XCTAssertEqual(MarkdownPreviewParser.parseEmbed("![[Note#Section]]"), .embed(target: "Note", heading: "Section"))
        XCTAssertNil(MarkdownPreviewParser.parseEmbed("[[Not an embed]]"))
        XCTAssertNil(MarkdownPreviewParser.parseEmbed("text ![[inline]] more"))
    }

    func testParsesMermaidFence() {
        let md = """
        ```mermaid
        graph TD
        A --> B
        ```
        """
        let blocks = MarkdownPreviewParser.parse(md)
        XCTAssertEqual(blocks, [.mermaid(code: "graph TD\nA --> B")])
    }

    func testParsesMathBlockAndCodeBlock() {
        let md = """
        $$
        E = mc^2
        $$

        ```swift
        let x = 1
        ```
        """
        let blocks = MarkdownPreviewParser.parse(md)
        XCTAssertEqual(blocks, [
            .mathBlock(tex: "E = mc^2"),
            .blank,
            .codeBlock(language: "swift", code: "let x = 1"),
        ])
    }

    func testSkipsFrontmatterAndParsesHeadings() {
        let md = """
        ---
        title: X
        ---
        # Hello
        Text here.
        """
        let blocks = MarkdownPreviewParser.parse(md)
        XCTAssertEqual(blocks, [.heading(level: 1, text: "Hello"), .paragraph(text: "Text here.")])
    }

    func testParsesTable() {
        let md = """
        | A | B |
        | --- | --- |
        | 1 | 2 |
        """
        let blocks = MarkdownPreviewParser.parse(md)
        XCTAssertEqual(blocks, [.table(rows: [["A", "B"], ["1", "2"]])])
    }

    func testMixedDocumentHasNoRawObsidianSyntax() {
        let md = """
        # Title

        > [!note] Heads up
        > Callout body

        ![[Embedded]]

        ```mermaid
        graph LR
        A --> B
        ```

        $$\\alpha + \\beta$$
        """
        let blocks = MarkdownPreviewParser.parse(md)
        XCTAssertTrue(blocks.contains { if case .callout = $0 { return true }; return false })
        XCTAssertTrue(blocks.contains { if case .embed = $0 { return true }; return false })
        XCTAssertTrue(blocks.contains { if case .mermaid = $0 { return true }; return false })
        XCTAssertTrue(blocks.contains { if case .mathBlock = $0 { return true }; return false })
    }
}

final class TransclusionResolverTests: XCTestCase {

    func testResolvesContent() {
        let result = TransclusionResolver.resolve(target: "Note", heading: nil, visited: []) { _ in "Hello" }
        XCTAssertEqual(result, .resolved(content: "Hello"))
    }

    func testDetectsCycle() {
        let result = TransclusionResolver.resolve(target: "A", heading: nil, visited: ["Root", "a"]) { _ in "x" }
        XCTAssertEqual(result, .cycle(target: "A"))
    }

    func testDepthLimit() {
        let visited = ["A", "B", "C", "D", "E"]
        let result = TransclusionResolver.resolve(target: "F", heading: nil, visited: visited) { _ in "x" }
        XCTAssertEqual(result, .tooDeep(target: "F"))
    }

    func testNotFound() {
        let result = TransclusionResolver.resolve(target: "Missing", heading: nil, visited: []) { _ in nil }
        XCTAssertEqual(result, .notFound(target: "Missing"))
    }

    func testHeadingSectionExtraction() {
        let content = """
        # Top
        intro
        ## Target
        body line
        ## Next
        other
        """
        let result = TransclusionResolver.resolve(target: "N", heading: "Target", visited: []) { _ in content }
        XCTAssertEqual(result, .resolved(content: "## Target\nbody line"))
    }
}

final class MathTypesetterTests: XCTestCase {

    func testGreekAndOperators() {
        XCTAssertEqual(MathTypesetter.render("\\alpha + \\beta \\times \\gamma"), "α + β × γ")
        XCTAssertEqual(MathTypesetter.render("a \\leq b \\neq c"), "a ≤ b ≠ c")
    }

    func testSuperscripts() {
        XCTAssertEqual(MathTypesetter.render("E = mc^2"), "E = mc²")
        XCTAssertEqual(MathTypesetter.render("x^{10}"), "x¹⁰")
    }

    func testSubscripts() {
        XCTAssertEqual(MathTypesetter.render("x_1 + x_2"), "x₁ + x₂")
    }

    func testFraction() {
        XCTAssertEqual(MathTypesetter.render("\\frac{a}{b}"), "(a⁄b)")
    }

    func testSqrt() {
        XCTAssertEqual(MathTypesetter.render("\\sqrt{x+1}"), "√(x+1)")
    }

    func testInlineMath() {
        XCTAssertEqual(MathTypesetter.renderInline(in: "Energy is $E = mc^2$ here."), "Energy is E = mc² here.")
        XCTAssertEqual(MathTypesetter.renderInline(in: "price is $5"), "price is $5")
    }
}

final class MermaidLiteTests: XCTestCase {

    func testParsesSimpleFlowchart() {
        let g = MermaidLite.parse("graph TD\nA[Start] --> B{Decision}\nB --> C[End]")
        XCTAssertNotNil(g)
        XCTAssertEqual(g?.direction, "TD")
        XCTAssertEqual(g?.nodes.count, 3)
        XCTAssertEqual(g?.nodes.first?.label, "Start")
        XCTAssertEqual(g?.edges.count, 2)
        XCTAssertEqual(g?.edges.first, MermaidLite.Edge(from: "A", to: "B"))
    }

    func testParsesEdgeLabels() {
        let g = MermaidLite.parse("flowchart LR\nA -->|yes| B")
        XCTAssertEqual(g?.edges.first?.label, "yes")
    }

    func testUnsupportedDiagramReturnsNil() {
        XCTAssertNil(MermaidLite.parse("sequenceDiagram\nAlice->>Bob: Hi"))
        XCTAssertNil(MermaidLite.parse("pie\n\"a\": 1"))
    }

    func testLayersForChain() {
        let g = MermaidLite.parse("graph TD\nA --> B\nB --> C")
        let layers = g!.layers()
        XCTAssertEqual(layers.count, 3)
        XCTAssertEqual(layers[0].map(\.id), ["A"])
        XCTAssertEqual(layers[2].map(\.id), ["C"])
    }

    func testCycleDoesNotHang() {
        let g = MermaidLite.parse("graph TD\nA --> B\nB --> A")
        XCTAssertNotNil(g)
        _ = g!.layers()
    }
}
