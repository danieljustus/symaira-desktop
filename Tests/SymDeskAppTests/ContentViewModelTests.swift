import XCTest
import SwiftUI
import Combine
import SymDeskCore
@testable import SymDesk

@MainActor
final class ContentViewModelTests: XCTestCase {
    private func makeNote(title: String, path: String) -> Note {
        let json = """
        {"title": "\(title)", "path": "\(path)", "sha256": "abc"}
        """
        return try! JSONDecoder().decode(Note.self, from: json.data(using: .utf8)!)
    }

    func testInitialState() {
        let model = ContentViewModel()
        XCTAssertEqual(model.displayMode, .dashboard)
        XCTAssertTrue(model.notes.isEmpty)
        XCTAssertNil(model.selectedNote)
        XCTAssertEqual(model.noteContent, "")
        XCTAssertFalse(model.canGoBack)
        XCTAssertFalse(model.canGoForward)
        XCTAssertTrue(model.appErrors.isEmpty)
        XCTAssertNil(model.loadError)
        XCTAssertFalse(model.isShowingPalette)
        XCTAssertFalse(model.isShowingInspector)
        XCTAssertFalse(model.isShowingPreview)
        XCTAssertFalse(model.isShowingAIDock)
        XCTAssertFalse(model.isShowingNewNoteSheet)
    }

    func testMutationTrackerForwardsObjectWillChange() {
        let model = ContentViewModel()
        var notifications = 0
        let cancellable = model.objectWillChange.sink { _ in
            notifications += 1
        }

        model.mutationTracker.testMarkInFlight("save:example.md")

        XCTAssertGreaterThan(notifications, 0)
        withExtendedLifetime(cancellable) {}
    }

    func testNavigationAndHistoryStack() {
        let model = ContentViewModel()
        let noteA = makeNote(title: "Note A", path: "/vault/Note A.md")
        let noteB = makeNote(title: "Note B", path: "/vault/Note B.md")
        model.notes = [noteA, noteB]

        XCTAssertEqual(model.displayMode, .dashboard)
        XCTAssertFalse(model.canGoBack)

        model.navigate(to: .vault, note: noteA)
        XCTAssertEqual(model.displayMode, .vault)
        XCTAssertEqual(model.selectedNote?.id, noteA.id)
        XCTAssertTrue(model.canGoBack)
        XCTAssertFalse(model.canGoForward)

        model.navigate(to: .docs, docFilter: "inbox")
        XCTAssertEqual(model.displayMode, .docs)
        XCTAssertEqual(model.docFilterID, "inbox")

        model.goBack()
        XCTAssertEqual(model.displayMode, .vault)
        XCTAssertEqual(model.selectedNote?.id, noteA.id)
        XCTAssertTrue(model.canGoForward)

        model.goBack()
        XCTAssertEqual(model.displayMode, .dashboard)
        XCTAssertTrue(model.canGoForward)

        model.goForward()
        XCTAssertEqual(model.displayMode, .vault)
        XCTAssertEqual(model.selectedNote?.id, noteA.id)
    }

    func testNavigateToNoteByTitle() {
        let model = ContentViewModel()
        let note = makeNote(title: "Meeting Notes", path: "/vault/Meeting Notes.md")
        model.notes = [note]
        model.noteLookup = ["meeting notes": note, "meeting notes.md": note]

        model.navigateToNote(title: "Meeting Notes")
        XCTAssertEqual(model.displayMode, .vault)
        XCTAssertEqual(model.selectedNote?.id, note.id)

        model.navigateToNote(title: "nonexistent")
        XCTAssertEqual(model.selectedNote?.id, note.id, "Should remain unchanged when note not found")
    }

    func testOpenNotebookSourcePath() {
        let model = ContentViewModel()
        let note = makeNote(title: "Project Spec", path: "specs/spec.md")
        model.notes = [note]

        model.openNotebookSourcePath("specs/spec.md")
        XCTAssertEqual(model.displayMode, .vault)
        XCTAssertEqual(model.selectedNote?.id, note.id)
    }

    func testApplyCreatedNoteSelectsAndNavigates() {
        let model = ContentViewModel()
        let existing = makeNote(title: "Old", path: "old.md")
        let created = makeNote(title: "New Note", path: "new.md")

        model.applyCreatedNote([existing, created], created: created, vaultPath: "/vault")
        XCTAssertEqual(model.notes.count, 2)
        XCTAssertEqual(model.selectedNote?.id, created.id)
        XCTAssertEqual(model.displayMode, .vault)
        XCTAssertFalse(model.folderTree.isEmpty)
    }

    func testDismissAppError() {
        let model = ContentViewModel()
        let err1 = AppErrorMessage(message: "First error")
        let err2 = AppErrorMessage(message: "Second error")
        model.appErrors = [err1, err2]

        model.dismissAppError(err1)
        XCTAssertEqual(model.appErrors.count, 1)
        XCTAssertEqual(model.appErrors.first?.id, err2.id)
    }

    func testEditorSurfaceToggles() {
        let model = ContentViewModel()
        XCTAssertFalse(model.isShowingAIDock)
        XCTAssertFalse(model.isShowingInspector)

        model.openAIDock()
        XCTAssertTrue(model.isShowingAIDock)
        XCTAssertTrue(model.isShowingInspector)

        model.toggleInspector()
        XCTAssertFalse(model.isShowingAIDock)
        XCTAssertFalse(model.isShowingInspector)

        model.toggleInspector()
        XCTAssertFalse(model.isShowingAIDock)
        XCTAssertTrue(model.isShowingInspector)
    }

    func testVaultRelativePath() {
        let model = ContentViewModel()
        let vault = "/Users/daniel/Vault"
        XCTAssertEqual(
            model.vaultRelativePath("/Users/daniel/Vault/docs/hello.md", vaultPath: vault),
            "docs/hello.md"
        )
        XCTAssertEqual(
            model.vaultRelativePath("/other/path.md", vaultPath: vault),
            "/other/path.md"
        )
        XCTAssertEqual(
            model.vaultRelativePath("plain.md", vaultPath: nil),
            "plain.md"
        )
    }

    func testIsConflicted() {
        let model = ContentViewModel()
        let normal = makeNote(title: "Normal", path: "Notes/Hello.md")
        let conflicted1 = makeNote(title: "Copy", path: "Notes/Hello 2.md")
        let conflicted2 = makeNote(title: "Conflict", path: "Notes/Hello (conflicted copy).md")

        XCTAssertFalse(model.isConflicted(normal))
        XCTAssertTrue(model.isConflicted(conflicted1))
        XCTAssertTrue(model.isConflicted(conflicted2))
    }

    func testDoctorSummaryText() {
        let model = ContentViewModel()
        XCTAssertEqual(model.doctorSummaryText, "Vault check unavailable — run `symdesk doctor`")

        let healthyReport = DoctorReport(
            overall: "ok",
            vault: DoctorReport.SubsystemStatus(status: "ok", message: "ready", path: "/vault"),
            sidecar: DoctorReport.SubsystemStatus(status: "ok", message: "indexed", path: nil),
            contract: DoctorReport.SubsystemStatus(status: "ok", message: "valid", path: nil),
            tools: DoctorReport.ToolAvailability(symvault: "ok"),
            versions: [:],
            ai: DoctorReport.AIReport(provider: "ollama", model: "llama3")
        )
        model.doctorReport = healthyReport
        XCTAssertTrue(model.doctorSummaryText.contains("Vault healthy"))
        XCTAssertTrue(model.doctorSummaryText.contains("AI: Ollama"))

        let warningReport = DoctorReport(
            overall: "warning",
            vault: DoctorReport.SubsystemStatus(status: "error", message: "unreadable", path: "/vault"),
            sidecar: DoctorReport.SubsystemStatus(status: "ok", message: "indexed", path: nil),
            contract: DoctorReport.SubsystemStatus(status: "ok", message: "valid", path: nil),
            tools: DoctorReport.ToolAvailability(),
            versions: [:],
            ai: DoctorReport.AIReport(provider: "claude", model: "sonnet")
        )
        model.doctorReport = warningReport
        XCTAssertTrue(model.doctorSummaryText.contains("Vault: Vault issue"))
        XCTAssertTrue(model.doctorSummaryText.contains("AI: Claude"))
    }

    func testAIDockContextBoundedExcerpt() {
        let model = ContentViewModel()
        let note = makeNote(title: "Note 1", path: "/vault/Note1.md")
        model.notes = [note]
        model.selectedNote = note
        model.noteContent = "Sample markdown content"

        let ctx = model.aiDockContext(vaultPath: "/vault")
        XCTAssertNotNil(ctx)
        XCTAssertEqual(ctx?.activeDocument, "Note1.md")
        XCTAssertEqual(ctx?.visibleExcerpt, "Sample markdown content")
    }
}
