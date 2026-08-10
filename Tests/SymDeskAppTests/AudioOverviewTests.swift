import XCTest
@testable import SymDesk

/// Regression tests for Audio Overview narration cleaning (issue #429).
/// The acceptance criteria require the narration to contain no raw
/// Markdown or wikilink syntax and to name the sources it was derived
/// from — these tests pin exactly that on the pure text-transform, without
/// touching the system speech synthesizer.
final class AudioOverviewTests: XCTestCase {
    func testStripsFrontmatter() {
        let content = "---\ntitle: Briefing\ncreated: \"2026-01-01\"\n---\nBody text here."
        let narration = AudioOverview.narrationText(fromArtifactContent: content, sources: [])
        XCTAssertFalse(narration.contains("title:"))
        XCTAssertFalse(narration.contains("---"))
        XCTAssertTrue(narration.contains("Body text here."))
    }

    func testStripsHeadingsAndEmphasis() {
        let content = "# Briefing\n\nThis is **important** and *notable*."
        let narration = AudioOverview.narrationText(fromArtifactContent: content, sources: [])
        XCTAssertFalse(narration.contains("#"))
        XCTAssertFalse(narration.contains("*"))
        XCTAssertTrue(narration.contains("This is important and notable."))
    }

    func testStripsWikilinksKeepingDisplayAlias() {
        let content = "See [[invoice-2026-03]] and [[project-plan|the plan]] for details."
        let narration = AudioOverview.narrationText(fromArtifactContent: content, sources: [])
        XCTAssertFalse(narration.contains("[["))
        XCTAssertFalse(narration.contains("]]"))
        XCTAssertTrue(narration.contains("invoice-2026-03"))
        XCTAssertTrue(narration.contains("the plan"))
        XCTAssertFalse(narration.contains("project-plan"))
    }

    func testStripsMarkdownLinksKeepingText() {
        let content = "Read the [full report](https://example.com/report) for context."
        let narration = AudioOverview.narrationText(fromArtifactContent: content, sources: [])
        XCTAssertFalse(narration.contains("]("))
        XCTAssertTrue(narration.contains("full report"))
        XCTAssertFalse(narration.contains("example.com"))
    }

    func testStripsCodeFencesAndInlineCode() {
        let content = "Run `symdesk notebook generate` or:\n```\nsymdesk ask \"question\"\n```\nThen review."
        let narration = AudioOverview.narrationText(fromArtifactContent: content, sources: [])
        XCTAssertFalse(narration.contains("`"))
        XCTAssertTrue(narration.contains("Run"))
        XCTAssertTrue(narration.contains("Then review."))
    }

    func testAppendsSingleSourceAttribution() {
        let narration = AudioOverview.narrationText(fromArtifactContent: "Body.", sources: ["docs/invoice-2026-03.md"])
        XCTAssertTrue(narration.contains("This overview is based on invoice-2026-03.md."))
    }

    func testAppendsMultiSourceAttribution() {
        let narration = AudioOverview.narrationText(
            fromArtifactContent: "Body.",
            sources: ["docs/a.md", "docs/b.md"]
        )
        XCTAssertTrue(narration.contains("This overview is based on: a.md, b.md."))
    }

    func testNoSourcesProducesNoAttributionSentence() {
        let narration = AudioOverview.narrationText(fromArtifactContent: "Body.", sources: [])
        XCTAssertFalse(narration.contains("based on"))
    }

    func testCollapsesExcessiveBlankLines() {
        let content = "First.\n\n\n\n\nSecond."
        let narration = AudioOverview.narrationText(fromArtifactContent: content, sources: [])
        XCTAssertFalse(narration.contains("\n\n\n"))
    }

    func testEmptyContentWithNoSourcesProducesEmptyNarration() {
        let narration = AudioOverview.narrationText(fromArtifactContent: "   ", sources: [])
        XCTAssertTrue(narration.isEmpty)
    }

    // MARK: - synthesize error paths (no real synthesis attempted)

    func testSynthesizeThrowsOnEmptyNarration() async {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".caf")
        do {
            try await AudioOverview.synthesize(text: "   ", voiceIdentifier: nil, rate: 0.5, to: url)
            XCTFail("expected emptyNarration error")
        } catch let error as AudioOverviewError {
            if case .emptyNarration = error {
                // expected
            } else {
                XCTFail("expected emptyNarration, got \(error)")
            }
        } catch {
            XCTFail("expected AudioOverviewError, got \(error)")
        }
    }

    // MARK: - generateAndStore vault-mode gate

    func testGenerateAndStoreRequiresLocalVault() async {
        do {
            _ = try await AudioOverview.generateAndStore(
                artifactContent: "Body.",
                sources: [],
                notebookID: "research-x",
                vaultRoot: nil,
                voiceIdentifier: nil,
                rate: 0.5
            )
            XCTFail("expected a local-vault-required error")
        } catch let error as AudioOverviewError {
            guard case .writeFailed(let reason) = error else {
                XCTFail("expected writeFailed, got \(error)")
                return
            }
            XCTAssertTrue(reason.contains("local vault"))
        } catch {
            XCTFail("expected AudioOverviewError, got \(error)")
        }
    }
}
