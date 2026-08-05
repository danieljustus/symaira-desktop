import SwiftUI

/// Fast note composer: one tap from the workspace to a cursor in an empty
/// note, autosave to a persistent draft (survives backgrounding and
/// force-quit), and full Markdown editing of existing notes with the raw
/// source preserved. Nothing touches the vault until the user finishes —
/// then the write layer takes over and queues offline.
struct MobileComposerView: View {
    @EnvironmentObject private var vault: MobileVaultStore
    @Environment(\.dismiss) private var dismiss

    /// The note being edited, or nil for a new note.
    let editingNote: MobileNote?
    /// Pre-chosen folder (vault-relative) for new notes.
    var initialFolder: String? = nil
    /// An autosaved draft to resume (from the unfinished-notes list).
    var resumeDraft: MobileDraftStore.Draft? = nil
    /// Dismiss callback (used by quick actions so the caller can navigate).
    var onFinished: (() -> Void)? = nil

    @State private var title: String = ""
    @State private var noteBody: String = ""
    @State private var folder: String = ""
    @State private var draftID: String = ""
    @State private var isSaving = false
    @State private var errorMessage: String?
    @State private var autosaveTask: Task<Void, Never>?

    private let draftStore = try? MobileDraftStore()

    init(editingNote: MobileNote? = nil, initialFolder: String? = nil, resumeDraft: MobileDraftStore.Draft? = nil, onFinished: (() -> Void)? = nil) {
        self.editingNote = editingNote
        self.initialFolder = initialFolder
        self.resumeDraft = resumeDraft
        self.onFinished = onFinished
    }

    var body: some View {
        NavigationStack {
            MobileBackdrop {
                VStack(spacing: 0) {
                    if editingNote == nil {
                        TextField("Title", text: $title)
                            .font(.headline)
                            .textFieldStyle(.plain)
                            .padding(.horizontal, 16)
                            .padding(.vertical, 12)
                            .background(.ultraThinMaterial)

                        if !folder.isEmpty {
                            HStack(spacing: 6) {
                                Image(systemName: "folder")
                                Text(folder)
                                Button { folder = "" } label: {
                                    Image(systemName: "xmark.circle.fill")
                                }
                                .buttonStyle(.plain)
                            }
                            .font(.caption)
                            .foregroundStyle(MobileTheme.textSecondary)
                            .padding(.horizontal, 16)
                            .padding(.bottom, 8)
                        }
                    }

                    TextEditor(text: $noteBody)
                        .font(.body)
                        .scrollContentBackground(.hidden)
                        .padding(.horizontal, 12)
                        .padding(.vertical, 8)
                        .autocorrectionDisabled(false)
                        .onChange(of: noteBody) { _, _ in scheduleAutosave() }
                        .onChange(of: title) { _, _ in scheduleAutosave() }
                        .overlay {
                            if noteBody.isEmpty {
                                Text(editingNote == nil
                                     ? "Write your note…"
                                     : "Edit Markdown… (raw source is preserved)")
                                    .foregroundStyle(MobileTheme.textMuted)
                                    .padding(.top, 22)
                                    .padding(.leading, 16)
                                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                                    .allowsHitTesting(false)
                            }
                        }

                    if let errorMessage {
                        Label(errorMessage, systemImage: "exclamationmark.triangle.fill")
                            .font(.caption)
                            .foregroundStyle(.red)
                            .padding(.horizontal, 16)
                            .padding(.vertical, 6)
                    }
                }
            }
            .navigationTitle(editingNote?.title ?? "New note")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") {
                        // Draft is kept for next time — never silently lost.
                        dismiss()
                        onFinished?()
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        Task { await finish() }
                    } label: {
                        if isSaving {
                            ProgressView()
                        } else {
                            Text(editingNote == nil ? "Save" : "Update")
                                .fontWeight(.semibold)
                        }
                    }
                    .disabled(isSaving || (editingNote == nil && title.trimmingCharacters(in: .whitespaces).isEmpty && noteBody.trimmingCharacters(in: .whitespaces).isEmpty))
                }
                if editingNote == nil {
                    ToolbarItem(placement: .topBarLeading) {
                        Menu {
                            ForEach(availableFolders, id: \.self) { candidate in
                                Button(candidate.isEmpty ? "Vault root" : candidate) {
                                    folder = candidate
                                    scheduleAutosave()
                                }
                            }
                        } label: {
                            Label(folder.isEmpty ? "Folder" : folder, systemImage: "folder")
                                .font(.caption)
                        }
                    }
                }
            }
        }
        .onAppear { loadInitialState() }
        .onDisappear { autosaveTask?.cancel() }
    }

    /// Folders observed in the vault, for the optional target-folder menu.
    private var availableFolders: [String] {
        var folders = Set<String>()
        for note in vault.notes {
            let dir = (note.path as NSString).deletingLastPathComponent
            if !dir.isEmpty { folders.insert(dir) }
        }
        folders.insert("")
        return folders.sorted()
    }

    private func loadInitialState() {
        if let draft = resumeDraft {
            // Resuming an autosaved unfinished note.
            title = draft.title
            noteBody = draft.body
            folder = draft.folder ?? ""
            draftID = draft.id
            return
        }
        if let note = editingNote {
            // Edit mode: raw source preserved verbatim, including
            // frontmatter, so nothing the mobile editor does not render
            // is lost on update.
            title = note.title
            noteBody = note.rawContent
            draftID = "edit-" + note.path.replacingOccurrences(of: "/", with: "-")
            folder = ""
        } else {
            title = ""
            noteBody = ""
            folder = initialFolder ?? ""
            draftID = "new-" + UUID().uuidString
        }
        // Restore an in-flight draft if the app was terminated mid-compose.
        if let stored = try? draftStore?.load(id: draftID), !stored.body.isEmpty {
            noteBody = stored.body
            if editingNote == nil, !stored.title.isEmpty { title = stored.title }
        }
    }

    private func scheduleAutosave() {
        autosaveTask?.cancel()
        autosaveTask = Task {
            try? await Task.sleep(for: .milliseconds(600))
            guard !Task.isCancelled else { return }
            persistDraft()
        }
    }

    /// Writes the draft to disk so force-quit cannot lose the text.
    private func persistDraft() {
        guard !noteBody.isEmpty || !title.isEmpty else { return }
        let draft = MobileDraftStore.Draft(
            id: draftID,
            title: editingNote == nil ? title : (editingNote?.title ?? title),
            body: noteBody,
            existingPath: editingNote?.path,
            folder: editingNote == nil ? folder : nil,
            updatedAt: Date()
        )
        try? draftStore?.save(draft)
    }

    private func finish() async {
        guard !noteBody.trimmingCharacters(in: .whitespaces).isEmpty || !title.trimmingCharacters(in: .whitespaces).isEmpty else {
            dismiss()
            onFinished?()
            return
        }
        isSaving = true
        errorMessage = nil
        do {
            if let note = editingNote {
                try await vault.enqueueUpdateNote(note, content: noteBody)
            } else {
                let folderForCreate = folder.isEmpty ? nil : folder
                _ = try await vault.enqueueCreateNote(title: title, body: noteBody, folder: folderForCreate)
            }
            try? await draftStore?.delete(id: draftID)
            isSaving = false
            dismiss()
            onFinished?()
        } catch {
            isSaving = false
            errorMessage = error.localizedDescription
        }
    }
}
