import Foundation
import XCTest
@testable import SymDeskMobile

/// Tests for the mobile filter/search-operator surface (#322): the
/// `internal/searchquery` grammar port, engine semantics (typed operator ≡
/// filter selection) and filter persistence.
final class MobileSearchQueryTests: XCTestCase {

    // MARK: - Parser (port parity with internal/searchquery)

    func testParsesKnownOperators() throws {
        let plan = try MobileSearchQueryParser.parse("tag:invoice type:receipt status:open path:archive")
        XCTAssertEqual(plan.filters.count, 4)
        XCTAssertEqual(plan.filters[0], .init(field: .tag, value: "invoice", negated: false))
        XCTAssertEqual(plan.filters[1], .init(field: .type, value: "receipt", negated: false))
        XCTAssertEqual(plan.filters[2], .init(field: .status, value: "open", negated: false))
        XCTAssertEqual(plan.filters[3], .init(field: .path, value: "archive", negated: false))
    }

    func testParsesBareTermsAndNegation() throws {
        let plan = try MobileSearchQueryParser.parse("Rechnung -Werbung")
        XCTAssertEqual(plan.terms.count, 2)
        XCTAssertEqual(plan.terms[0], .init(value: "Rechnung", phrase: false, negated: false))
        XCTAssertEqual(plan.terms[1], .init(value: "Werbung", phrase: false, negated: true))
    }

    func testParsesQuotedPhrase() throws {
        let plan = try MobileSearchQueryParser.parse("\"July invoice\"")
        XCTAssertEqual(plan.terms.count, 1)
        XCTAssertEqual(plan.terms[0], .init(value: "July invoice", phrase: true, negated: false))
    }

    func testParsesRegex() throws {
        let plan = try MobileSearchQueryParser.parse("/rechnung \\d+/")
        XCTAssertEqual(plan.regexes.count, 1)
        XCTAssertEqual(plan.regexes[0].pattern, "rechnung \\d+")
    }

    func testUnknownOperatorIsError() {
        XCTAssertThrowsError(try MobileSearchQueryParser.parse("foo:bar")) { error in
            XCTAssertEqual(error as? MobileSearchQueryParser.ParseError, .unknownOperator("foo:"))
        }
    }

    func testEmptyOperatorValueIsError() {
        XCTAssertThrowsError(try MobileSearchQueryParser.parse("tag:")) { error in
            XCTAssertEqual(error as? MobileSearchQueryParser.ParseError, .operatorRequiresValue("tag:"))
        }
    }

    func testLoneColonFallsThroughAsPlainText() throws {
        let plan = try MobileSearchQueryParser.parse("12:30 meeting")
        XCTAssertTrue(plan.filters.isEmpty)
        XCTAssertEqual(plan.terms.count, 2)
    }

    func testNegationWithoutTermIsError() {
        XCTAssertThrowsError(try MobileSearchQueryParser.parse("Rechnung -")) { error in
            XCTAssertEqual(error as? MobileSearchQueryParser.ParseError, .negationWithoutTerm)
        }
    }

    func testUnterminatedPhraseIsError() {
        XCTAssertThrowsError(try MobileSearchQueryParser.parse("\"never closed"))
    }

    // MARK: - Engine (typed operator ≡ filter selection)

    private func note(
        _ name: String,
        title: String,
        tags: [String] = [],
        documentType: String = "",
        correspondent: String = "",
        status: String = "",
        documentDate: String = "",
        body: String = "body text"
    ) throws -> MobileNote {
        let root = URL(fileURLWithPath: "/tmp/SymDeskMobileVault", isDirectory: true)
        let source = """
        ---
        title: "\(title)"
        tags: [\(tags.map { "\"\($0)\"" }.joined(separator: ", "))]
        document_type: "\(documentType)"
        correspondent: "\(correspondent)"
        status: "\(status)"
        document_date: "\(documentDate)"
        ---

        \(body)
        """
        return try MobileVaultParser.parse(
            data: Data(source.utf8),
            fileURL: root.appendingPathComponent("\(name).md"),
            root: root,
            modifiedAt: .now
        )
    }

    func testTypedTagOperatorEqualsTagFilterSelection() throws {
        let notes = [
            try note("a", title: "Invoice", tags: ["invoice"], documentType: "receipt"),
            try note("b", title: "Letter", tags: ["mail"]),
        ]
        // Typed operator.
        let typed = try MobileSearchQueryParser.parse("tag:invoice")
        let viaOperator = MobileSearchFilterEngine.filter(notes, plan: typed, ui: .none)
        XCTAssertEqual(viaOperator.map(\.path), ["a.md"])
        // Equivalent filter chip selection.
        var ui = MobileSearchFilterEngine.UIFilters()
        ui.tags = ["invoice"]
        let viaChip = MobileSearchFilterEngine.filter(notes, plan: .init(), ui: ui)
        XCTAssertEqual(viaChip.map(\.path), viaOperator.map(\.path))
    }

    func testTypedTypeOperatorEqualsTypeFilterSelection() throws {
        let notes = [
            try note("a", title: "Invoice", documentType: "receipt"),
            try note("b", title: "Contract", documentType: "contract"),
        ]
        let typed = try MobileSearchQueryParser.parse("type:receipt")
        let viaOperator = MobileSearchFilterEngine.filter(notes, plan: typed, ui: .none)
        XCTAssertEqual(viaOperator.map(\.path), ["a.md"])

        var ui = MobileSearchFilterEngine.UIFilters()
        ui.documentTypes = ["receipt"]
        let viaChip = MobileSearchFilterEngine.filter(notes, plan: .init(), ui: ui)
        XCTAssertEqual(viaChip.map(\.path), viaOperator.map(\.path))
    }

    func testFiltersCombineWithTextQuery() throws {
        let notes = [
            try note("a", title: "Invoice July", tags: ["invoice"], body: "Rechnung vom Juli"),
            try note("b", title: "Invoice June", tags: ["invoice"], body: "Rechnung vom Juni"),
            try note("c", title: "Other", tags: ["invoice"], body: "Ganz anderer Inhalt"),
        ]
        var ui = MobileSearchFilterEngine.UIFilters()
        ui.tags = ["invoice"]
        let plan = try MobileSearchQueryParser.parse("Juli")
        let results = MobileSearchFilterEngine.filter(notes, plan: plan, ui: ui)
        XCTAssertEqual(results.map(\.path), ["a.md"])
    }

    func testNegatedTermExcludes() throws {
        let notes = [
            try note("a", title: "Invoice", body: "Rechnung Juli"),
            try note("b", title: "Invoice", body: "Rechnung ohne Werbung"),
        ]
        let plan = try MobileSearchQueryParser.parse("Rechnung -Werbung")
        let results = MobileSearchFilterEngine.filter(notes, plan: plan, ui: .none)
        XCTAssertEqual(results.map(\.path), ["a.md"])
    }

    func testPrefixTermMatchesTokenPrefix() throws {
        let notes = [
            try note("a", title: "Rechnung", body: "x"),
            try note("b", title: "Anders", body: "x"),
        ]
        let plan = try MobileSearchQueryParser.parse("rech")
        let results = MobileSearchFilterEngine.filter(notes, plan: plan, ui: .none)
        XCTAssertEqual(results.map(\.path), ["a.md"])
    }

    func testPhraseRequiresExactNormalizedSequence() throws {
        let notes = [
            try note("a", title: "July invoice", body: "x"),
            try note("b", title: "July", body: "invoice"),
        ]
        let plan = try MobileSearchQueryParser.parse("\"July invoice\"")
        let results = MobileSearchFilterEngine.filter(notes, plan: plan, ui: .none)
        XCTAssertEqual(results.map(\.path), ["a.md"])
    }

    func testCorrespondentFilterAndDateRange() throws {
        let notes = [
            try note("a", title: "A", correspondent: "Strom GmbH", documentDate: "2026-07-01"),
            try note("b", title: "B", correspondent: "Gas AG", documentDate: "2026-07-15"),
            try note("c", title: "C", correspondent: "Strom GmbH", documentDate: "2025-12-01"),
        ]
        var ui = MobileSearchFilterEngine.UIFilters()
        ui.correspondents = ["Strom GmbH"]
        ui.dateRange = "2026-01-01"..."2026-12-31"
        let results = MobileSearchFilterEngine.filter(notes, plan: .init(), ui: ui)
        XCTAssertEqual(results.map(\.path), ["a.md"])
    }

    func testNotesOnlyExcludesDocuments() throws {
        let notes = [
            try note("a", title: "Note", documentType: ""),
            try note("b", title: "Doc", documentType: "receipt"),
        ]
        var ui = MobileSearchFilterEngine.UIFilters()
        ui.notesOnly = true
        let results = MobileSearchFilterEngine.filter(notes, plan: .init(), ui: ui)
        XCTAssertEqual(results.map(\.path), ["a.md"])
    }

    func testFacetsDerivedFromVaultContent() throws {
        let notes = [
            try note("a", title: "A", tags: ["invoice", "2026"], documentType: "receipt", correspondent: "Strom GmbH", status: "open"),
            try note("b", title: "B", tags: ["invoice"], documentType: "contract", correspondent: "Gas AG", status: "paid"),
        ]
        let facets = MobileSearchFilterEngine.facets(of: notes)
        XCTAssertEqual(facets.tags, ["invoice", "2026"])
        XCTAssertEqual(facets.documentTypes, ["receipt", "contract"])
        XCTAssertEqual(facets.correspondents, ["Strom GmbH", "Gas AG"])
        XCTAssertEqual(facets.statuses, ["open", "paid"])
    }

    // MARK: - Persistence

    func testActiveFiltersRoundTripThroughUserDefaults() throws {
        let key = MobileActiveFilters.storageKey
        UserDefaults.standard.removeObject(forKey: key)
        defer { UserDefaults.standard.removeObject(forKey: key) }

        var filters = MobileActiveFilters()
        filters.tags = ["invoice"]
        filters.correspondents = ["Strom GmbH"]
        filters.documentTypes = ["receipt"]
        filters.dateFrom = "2026-01-01"
        filters.save()

        let loaded = MobileActiveFilters.load()
        XCTAssertEqual(loaded, filters)
        XCTAssertTrue(loaded.isActive)
        XCTAssertEqual(loaded.uiFilters.tags, ["invoice"])
        XCTAssertEqual(loaded.uiFilters.dateRange, "2026-01-01"..."9999-12-31")
    }
}
