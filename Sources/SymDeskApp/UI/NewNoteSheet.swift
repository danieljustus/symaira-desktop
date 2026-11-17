import SwiftUI
import SymairaTheme
import SymDeskCore

// MARK: - New Note Sheet

struct NewNoteSheet: View {
    @Binding var isPresented: Bool
    let core: DeskCore
    /// Called with the refreshed note list and the newly created note so the
    /// caller can refresh its lists and select it immediately (issue #647).
    var onCreated: (([Note], Note?) -> Void)? = nil

    @State private var title = ""
    @State private var isCreating = false
    @State private var errorMessage: String?
    @FocusState private var isTitleFocused: Bool

    var body: some View {
        SymairaSheetScaffold(
            title: "New Note",
            dismiss: { if !isCreating { isPresented = false } },
            primaryTitle: "Create Note",
            primaryDisabled: title.trimmingCharacters(in: .whitespaces).isEmpty || isCreating,
            primary: { createNote() }
        ) {
            HStack(spacing: SymairaSpacing.small) {
                SymairaIconView(.documentAdd, size: 20)
                    .foregroundStyle(SymairaTokens.ink2)
                    .accessibilityHidden(true)
                TextField("Note title", text: $title)
                    .textFieldStyle(.symaira(isFocused: isTitleFocused))
                    .focused($isTitleFocused)
                    .onSubmit { createNote() }
                    .disabled(isCreating)
                if !title.isEmpty {
                    Button {
                        title = ""
                    } label: {
                        SymairaIconView(.close, size: 16)
                            .foregroundStyle(SymairaTokens.ink3)
                    }
                    .buttonStyle(.plain)
                    .help("Clear")
                    .accessibilityLabel("Clear")
                }
            }

            if isCreating {
                HStack(spacing: SymairaSpacing.small) {
                    ProgressView()
                        .controlSize(.small)
                    Text("Creating note…")
                        .symairaText(.secondary)
                }
                .accessibilityElement(children: .combine)
            }
            if let err = errorMessage {
                SymairaNotice(message: err, tone: .critical, onDismiss: { errorMessage = nil })
            }
        }
        .frame(width: 440, height: 300)
        .onAppear { isTitleFocused = true }
    }

    private func createNote() {
        guard !title.trimmingCharacters(in: .whitespaces).isEmpty, !isCreating else { return }
        isCreating = true
        errorMessage = nil
        Task {
            do {
                let path = try await core.noteNew(title: title.trimmingCharacters(in: .whitespaces))
                // Refresh and report immediately (issue #647): relying on the
                // file watcher left the new note invisible until restart.
                let notes = try await core.listFiles()
                let created = notes.first { $0.path == path }
                await MainActor.run {
                    isPresented = false
                    isCreating = false
                    onCreated?(notes, created)
                }
            } catch {
                await MainActor.run {
                    errorMessage = "Could not create note: \(error.localizedDescription)"
                    isCreating = false
                }
            }
        }
    }
}
