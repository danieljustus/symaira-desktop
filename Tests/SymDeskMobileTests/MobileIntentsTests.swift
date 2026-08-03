import Foundation
import XCTest
@testable import SymDeskMobile

/// Tests for the App Intents / quick actions / widget plumbing (#328):
/// the shared action store (Shortcuts, widget buttons, quick actions),
/// the App-Group recents store the widget renders, and the intent wiring.
final class MobileIntentsTests: XCTestCase {

    private let testSuite = "symdesk.mobile.tests.\(UUID().uuidString)"

    override func setUp() {
        super.setUp()
        MobileAppActionStore.suiteName = testSuite
        MobileRecentsStore.suiteName = testSuite
        UserDefaults(suiteName: testSuite)?.removePersistentDomain(forName: testSuite)
    }

    override func tearDown() {
        UserDefaults(suiteName: testSuite)?.removePersistentDomain(forName: testSuite)
        MobileAppActionStore.suiteName = "group.com.symaira.desktop.ios"
        MobileRecentsStore.suiteName = "group.com.symaira.desktop.ios"
        super.tearDown()
    }

    // MARK: - App action store

    func testActionStoreRoundtrip() {
        XCTAssertNil(MobileAppActionStore.pending())
        MobileAppActionStore.set(.newNote)
        XCTAssertEqual(MobileAppActionStore.pending(), .newNote)
        MobileAppActionStore.clear()
        XCTAssertNil(MobileAppActionStore.pending())
    }

    func testActionStoreOverwritesPreviousAction() {
        MobileAppActionStore.set(.scanDocument)
        MobileAppActionStore.set(.newNote)
        XCTAssertEqual(MobileAppActionStore.pending(), .newNote, "last action wins")
    }

    func testActionSurvivesStoreRecreation() {
        MobileAppActionStore.set(.scanDocument)
        // Same suite, fresh access — simulates a widget tap parking an
        // action that the app reads after launch.
        XCTAssertEqual(MobileAppActionStore.pending(), .scanDocument)
    }

    // MARK: - Recents store (widget data source)

    func testRecentsDedupAndOrder() {
        MobileRecentsStore.record(path: "notes/a.md", title: "A")
        MobileRecentsStore.record(path: "notes/b.md", title: "B")
        MobileRecentsStore.record(path: "notes/a.md", title: "A")
        let recents = MobileRecentsStore.read()
        XCTAssertEqual(recents.map(\.path), ["notes/a.md", "notes/b.md"], "re-record moves to front, no duplicates")
    }

    func testRecentsCapAtTen() {
        for i in 0..<14 {
            MobileRecentsStore.record(path: "notes/n\(i).md", title: "N\(i)")
        }
        let recents = MobileRecentsStore.read()
        XCTAssertEqual(recents.count, 10)
        XCTAssertEqual(recents.first?.path, "notes/n13.md")
        XCTAssertEqual(recents.last?.path, "notes/n4.md")
    }

    func testRecentsPersistAcrossStoreAccesses() {
        MobileRecentsStore.record(path: "inbox/x.md", title: "X")
        XCTAssertEqual(MobileRecentsStore.read().map(\.title), ["X"])
    }

    func testRecentsClearRemovesEverything() {
        MobileRecentsStore.record(path: "a.md", title: "A")
        MobileRecentsStore.clear()
        XCTAssertTrue(MobileRecentsStore.read().isEmpty)
    }

    func testRecentsLegacyPathArrayMigration() {
        // Simulate the pre-#328 storage: plain paths in standard defaults
        // under the same key, empty shared suite.
        let legacy = ["notes/Alte Notiz.md", "inbox/Rechnung 2026-07.pdf"]
        UserDefaults.standard.set(legacy, forKey: "symdesk.mobile.recently-opened.v1")
        defer { UserDefaults.standard.removeObject(forKey: "symdesk.mobile.recently-opened.v1") }

        let recents = MobileRecentsStore.read()
        XCTAssertEqual(recents.map(\.path), legacy)
        XCTAssertEqual(recents[0].title, "Alte Notiz", "title derived from the file name")
        XCTAssertEqual(recents[1].title, "Rechnung 2026-07")
    }

    // MARK: - Intent contract

    func testIntentsCarryTheExpectedActions() async throws {
        _ = try await OpenNewNoteIntent().perform()
        XCTAssertEqual(MobileAppActionStore.pending(), .newNote)
        MobileAppActionStore.clear()

        _ = try await OpenScanDocumentIntent().perform()
        XCTAssertEqual(MobileAppActionStore.pending(), .scanDocument)
        MobileAppActionStore.clear()
    }
}
