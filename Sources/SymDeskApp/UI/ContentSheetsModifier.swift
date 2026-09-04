import SwiftUI
import SymDeskCore

// MARK: - Content Sheets & Dialogs

/// Manages modal sheets, dialogs, and alerts presented from ContentView.
struct ContentSheetsModifier: ViewModifier {
    @ObservedObject var model: ContentViewModel
    let core: DeskCore

    func body(content: Content) -> some View {
        content
            .alert("Couldn't Import File", isPresented: Binding(
                get: { model.ingestFailure != nil },
                set: { isPresented in if !isPresented { model.ingestFailure = nil } }
            )) {
                Button("Retry") {
                    if let url = model.ingestFailure?.url {
                        Task { await model.ingestFile(url, core: core) }
                    }
                }
                Button("Dismiss", role: .cancel) { model.ingestFailure = nil }
            } message: {
                Text(model.ingestFailure?.message ?? "")
            }
            .sheet(isPresented: $model.isShowingViewEditor) {
                DbViewEditor(existing: model.editingDbView) {
                    Task { await model.fetchViews(core: core) }
                }
            }
            .sheet(isPresented: $model.isShowingPalette) {
                CommandPalette(
                    isPresented: $model.isShowingPalette,
                    allNotes: $model.notes,
                    onSelectNote: { note in
                        model.navigate(to: .vault, note: note)
                    },
                    onSelectSearchResult: { result in
                        model.navigate(to: .docs, deepLinkPath: result.path, deepLinkAnchor: result.anchor)
                    }
                )
            }
            .sheet(isPresented: $model.isShowingNewNoteSheet) {
                NewNoteSheet(
                    isPresented: $model.isShowingNewNoteSheet,
                    core: core,
                    onCreated: { (refreshedNotes: [Note], created: Note?) -> Void in
                        model.applyCreatedNote(refreshedNotes, created: created, vaultPath: core.vaultPath)
                    }
                )
            }
            .confirmationDialog(
                "Move “\(model.pendingTrashNote?.title ?? "")” to Trash?",
                isPresented: Binding(
                    get: { model.pendingTrashNote != nil },
                    set: { if !$0 { model.pendingTrashNote = nil } }
                ),
                titleVisibility: .visible
            ) {
                Button("Move to Trash", role: .destructive) {
                    if let note = model.pendingTrashNote {
                        Task { await model.moveToTrash(note, core: core) }
                    }
                    model.pendingTrashNote = nil
                }
                Button("Cancel", role: .cancel) { model.pendingTrashNote = nil }
            } message: {
                Text("The note moves to the vault trash and can be restored from the Trash screen. Your files stay on disk.")
            }
    }
}

extension View {
    func contentSheets(model: ContentViewModel, core: DeskCore) -> some View {
        modifier(ContentSheetsModifier(model: model, core: core))
    }
}
