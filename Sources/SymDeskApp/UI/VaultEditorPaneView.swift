import SwiftUI
import SymairaTheme
import SymDeskCore

/// Renders the note editor, sync conflict, save error, and load error banners,
/// or the empty selection placeholder when no note is open.
struct VaultEditorPaneView: View {
    @ObservedObject var model: ContentViewModel
    @Binding var isBlockMode: Bool
    @EnvironmentObject var core: DeskCore

    var body: some View {
        if let note = model.selectedNote {
            VStack(spacing: 0) {
                if model.isConflicted(note) {
                    HStack(spacing: 8) {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .foregroundStyle(SymairaTheme.goldPrimary)
                        Text("iCloud sync conflict detected")
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.goldSecondary)
                        Spacer()
                        Button("Keep Mine") {
                            Task { await model.resolveConflict(note: note, action: "keep-mine", core: core) }
                        }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                        .disabled(model.mutationTracker.isInFlight(model.conflictActionID(note: note, action: "keep-mine")))
                        Button("Keep Theirs") {
                            Task { await model.resolveConflict(note: note, action: "keep-theirs", core: core) }
                        }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                        .disabled(model.mutationTracker.isInFlight(model.conflictActionID(note: note, action: "keep-theirs")))
                    }
                    .padding(8)
                    .background(SymairaTheme.goldPrimary.opacity(0.12))
                    .cornerRadius(6)
                    .overlay(
                        RoundedRectangle(cornerRadius: 6)
                            .stroke(SymairaTheme.borderGlassHover, lineWidth: 1)
                    )
                    .padding(.horizontal)
                    .padding(.top, 8)
                    .asyncActionAlert(model.mutationTracker, id: model.conflictActionID(note: note, action: "keep-mine"), title: "Couldn't Resolve Conflict") {
                        Task { await model.resolveConflict(note: note, action: "keep-mine", core: core) }
                    }
                    .asyncActionAlert(model.mutationTracker, id: model.conflictActionID(note: note, action: "keep-theirs"), title: "Couldn't Resolve Conflict") {
                        Task { await model.resolveConflict(note: note, action: "keep-theirs", core: core) }
                    }
                }

                if let saveError = model.mutationTracker.failureMessage(for: model.saveActionID(note)) {
                    HStack(spacing: 8) {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .foregroundStyle(.red)
                        Text("Save failed: \(saveError)")
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textSecondary)
                        Spacer()
                        Button("Retry") {
                            Task { await model.performSave(note: note, content: model.noteContent, core: core) }
                        }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                        Button(action: { model.mutationTracker.clearFailure(for: model.saveActionID(note)) }) {
                            Image(systemName: "xmark")
                        }
                        .buttonStyle(.plain)
                        .foregroundStyle(SymairaTheme.textSecondary)
                    }
                    .padding(8)
                    .background(Color.red.opacity(0.12))
                    .cornerRadius(6)
                    .padding(.horizontal)
                    .padding(.top, 8)
                }

                if let message = model.loadError {
                    LoadErrorBanner(
                        message: message,
                        showsRemoveAction: model.loadErrorNote != nil,
                        onRetry: {
                            model.loadError = nil
                            if let note = model.selectedNote {
                                Task { await model.loadContent(for: note, core: core) }
                            }
                        },
                        onRemoveFromIndex: {
                            Task {
                                await model.reconcileIndex(core: core)
                                if model.selectedNote?.id == model.loadErrorNote?.id {
                                    model.selectedNote = nil
                                    model.noteContent = ""
                                }
                                model.loadError = nil
                                model.loadErrorNote = nil
                            }
                        },
                        onDismiss: { model.loadError = nil }
                    )
                }

                if model.loadError == nil {
                    HStack(spacing: 0) {
                        if isBlockMode {
                            BlockEditorView(text: $model.noteContent)
                                .padding(.top, 4)
                        } else {
                            MarkdownEditorView(text: $model.noteContent, onLinkClick: { targetTitle in
                                model.navigateToNote(title: targetTitle)
                            }, core: core, vaultRoot: core.vaultPath, onImageError: { message in
                                model.appErrors.append(AppErrorMessage(
                                    message: message,
                                    detail: "The image was not inserted."
                                ))
                            })
                        }
                        
                        // Dummy view to attach onChange (since we use if/else for the editor)
                        Color.clear.frame(width: 0, height: 0)
                            .onChange(of: model.noteContent) { _, newValue in
                                // Do not autosave into a buffer whose backing file
                                // failed to load — the save would resurrect a dead
                                // path or clobber the real file (issue #650).
                                guard model.loadError == nil else { return }
                                model.debouncedSave(note: note, content: newValue, core: core)
                            }

                        if model.isShowingPreview {
                            Divider()
                            MarkdownPreviewView(
                                text: model.noteContent,
                                resolveNote: { target in model.resolveNoteContent(target, vaultPath: core.vaultPath, isRemote: core.isRemote) },
                                visited: [note.title],
                                onLinkClick: { targetTitle in
                                    model.navigateToNote(title: targetTitle)
                                }
                            )
                            .frame(maxWidth: .infinity)
                        }
                    }
                }
            }
            .navigationTitle(note.title)
            .task(id: note.id) {
                await model.loadContent(for: note, core: core)
                await model.loadBacklinks(for: note, core: core)
            }
        } else {
            ContentUnavailableView {
                Label("No Note Selected", systemImage: "doc.text")
            } description: {
                Text("Choose a note in the sidebar or open Quick Search with ⌘K.")
            } actions: {
                Button("Open Quick Search") { model.isShowingPalette = true }
                    .buttonStyle(SymairaPrimaryButtonStyle())
            }
            .frame(maxWidth: 460)
            .padding(32)
            .symDeskLiquidGlass(cornerRadius: 20)
        }
    }
}
