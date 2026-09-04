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
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Image(systemName: "doc.badge.plus")
                    .symairaText(.title)
                    .foregroundColor(SymairaTheme.goldPrimary)
                TextField("Note title", text: $title)
                    .textFieldStyle(.plain)
                    .symairaText(.title)
                    .foregroundColor(SymairaTheme.textPrimary)
                    .focused($isTitleFocused)
                    .onSubmit { createNote() }
                    .disabled(isCreating)
                if !title.isEmpty {
                    Button {
                        title = ""
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(SymairaTheme.textMuted)
                    }
                    .buttonStyle(.plain)
                    .help("Clear")
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .symDeskLiquidGlass(cornerRadius: 14, prominence: .elevated)
            .padding(16)
            
            HStack {
                if isCreating {
                    ProgressView()
                        .controlSize(.small)
                        .padding(.horizontal, 8)
                }
                if let err = errorMessage {
                    Text(err)
                        .symairaText(.caption)
                        .foregroundColor(.red)
                }
                Spacer()
                Button("Cancel") { isPresented = false }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                    .disabled(isCreating)
                Button("Create") { createNote() }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .tint(SymairaTheme.goldPrimary)
                    .disabled(title.isEmpty || isCreating)
            }
            .padding(.horizontal, 24)
            .padding(.bottom, 16)
        }
        .background { SymairaScreen { Color.clear } }
        .frame(width: 440, height: 160)
        .onAppear { isTitleFocused = true }
    }
    
    private func createNote() {
        guard !title.trimmingCharacters(in: .whitespaces).isEmpty else { return }
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
