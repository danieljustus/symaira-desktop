import SwiftUI
import UIKit

/// Inline-AI sheet: choose an intent (Summarise / Rewrite / Continue),
/// optionally select text in the editor, stream the suggestion, and only
/// then accept — nothing is written before the explicit Accept. After an
/// accept, Undo restores the pre-session raw file (frontmatter included)
/// through the write layer.
struct MobileInlineAIView: View {
    @Environment(\.dismiss) private var dismiss
    @StateObject private var model: MobileInlineAIModel

    let note: MobileNote

    @State private var editorText: String
    @State private var selectedRange: NSRange?
    @State private var intent: MobileInlineAIIntent = .summarize

    init(note: MobileNote, vault: MobileVaultStore) {
        self.note = note
        _editorText = State(initialValue: note.body)
        _model = StateObject(wrappedValue: MobileInlineAIModel(
            // The session's original is the full raw file (frontmatter
            // included): the editor and the transform operate on the
            // parsed body only, but accept rebuilds the complete file and
            // undo restores exactly what was on disk.
            original: note.rawContent,
            runner: MobileInlineAIRunner(
                primary: {
                    MobileAIProviderFactory.select(
                        connection: MobileServerConfig.connection(),
                        vaultNotes: vault.notes
                    ).provider
                },
                onDeviceFallback: {
                    let onDevice = MobileOnDeviceAIProvider(vaultNotes: vault.notes)
                    return onDevice.isAvailable ? onDevice : nil
                }
            ),
            save: { content in
                try await vault.enqueueUpdateNote(note, content: content)
            }
        ))
    }

    var body: some View {
        NavigationStack {
            MobileBackdrop {
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        providerRow
                        intentPicker
                        sourceEditor
                        actionRow

                        if model.hasResult || model.phase == .streaming {
                            comparison
                        }

                        if let errorMessage = model.errorMessage {
                            Label(errorMessage, systemImage: "exclamationmark.triangle.fill")
                                .font(.caption)
                                .foregroundStyle(.red)
                        }

                        acceptRow
                    }
                    .padding(16)
                }
            }
            .navigationTitle("AI actions")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") {
                        model.cancel()
                        dismiss()
                    }
                }
            }
            .onDisappear { model.cancel() }
        }
    }

    // MARK: - Sections

    private var providerRow: some View {
        HStack(spacing: 8) {
            Image(systemName: model.isOnDevice ? "cpu" : "server.rack")
                .font(.caption)
                .foregroundStyle(MobileTheme.textSecondary)
            Text(model.activeProviderName ?? "No AI provider")
                .font(.caption.weight(.semibold))
                .foregroundStyle(MobileTheme.textSecondary)
            Spacer()
            if model.phase == .streaming {
                ProgressView()
                    .controlSize(.small)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 7)
        .background(MobileTheme.card, in: Capsule())
        .accessibilityElement(children: .combine)
    }

    private var intentPicker: some View {
        Picker("Intent", selection: $intent) {
            ForEach(MobileInlineAIIntent.allCases) { candidate in
                Label(candidate.displayName, systemImage: candidate.systemImage)
                    .tag(candidate)
            }
        }
        .pickerStyle(.segmented)
        .disabled(model.phase == .streaming)
    }

    private var sourceEditor: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("Text to transform")
                    .font(.caption.weight(.medium))
                    .foregroundStyle(MobileTheme.textMuted)
                Spacer()
                if let selectedRange, selectedRange.length > 0 {
                    Label("\(selectedRange.length) chars selected", systemImage: "highlighter")
                        .font(.caption2)
                        .foregroundStyle(MobileTheme.goldSoft)
                    Button {
                        self.selectedRange = nil
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .font(.caption2)
                            .foregroundStyle(MobileTheme.textMuted)
                    }
                    .buttonStyle(.plain)
                } else {
                    Text("Whole text")
                        .font(.caption2)
                        .foregroundStyle(MobileTheme.textMuted)
                }
            }
            MobileSelectionTextEditor(
                text: $editorText,
                onSelection: { selectedRange = $0 }
            )
            .frame(minHeight: 120, maxHeight: 220)
            .padding(8)
            .background(MobileTheme.card, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay {
                if editorText.isEmpty {
                    Text("Select text to transform, or leave empty for the whole text…")
                        .font(.subheadline)
                        .foregroundStyle(MobileTheme.textMuted)
                        .padding(.leading, 18)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .allowsHitTesting(false)
                }
            }
            .disabled(model.phase == .streaming || model.accepted)
        }
    }

    private var actionRow: some View {
        Button {
            runTransform()
        } label: {
            Label("\(intent.displayName) text", systemImage: intent.systemImage)
                .frame(maxWidth: .infinity)
        }
        .buttonStyle(.borderedProminent)
        .tint(MobileTheme.gold)
        .foregroundStyle(.black)
        .disabled(
            model.phase == .streaming
                || editorText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        )
    }

    private var comparison: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Original")
                .font(.caption.weight(.semibold))
                .foregroundStyle(MobileTheme.textMuted)
            Text(model.transformedSource)
                .font(.footnote)
                .foregroundStyle(MobileTheme.textSecondary)
                .lineLimit(6)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(12)
                .background(MobileTheme.backgroundRaised, in: RoundedRectangle(cornerRadius: 12, style: .continuous))

            Text("Suggestion")
                .font(.caption.weight(.semibold))
                .foregroundStyle(MobileTheme.goldSoft)
            Text(model.suggestion)
                .font(.subheadline)
                .foregroundStyle(MobileTheme.textPrimary)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(12)
                .background(MobileTheme.gold.opacity(0.08), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
    }

    private var acceptRow: some View {
        HStack(spacing: 10) {
            if model.accepted {
                Button(role: .destructive) {
                    Task { await model.undo() }
                } label: {
                    Label("Undo", systemImage: "arrow.uturn.backward")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
                .disabled(model.phase == .streaming)
            } else {
                Button {
                    Task { await model.accept(currentText: editorText, selectedRange: selectedRange) }
                } label: {
                    Label("Accept & save", systemImage: "checkmark.circle")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .tint(MobileTheme.gold)
                .foregroundStyle(.black)
                .disabled(!model.hasResult || model.phase == .streaming || model.accepted)
            }

            Button(role: .cancel) {
                model.discardResult()
            } label: {
                Text("Discard")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.bordered)
            .disabled(model.phase == .streaming && !model.hasResult)
        }
    }

    private func runTransform() {
        let text = selectedText ?? editorText
        model.start(intent: intent, text: text, selectedRange: selectedRange)
    }

    private var selectedText: String? {
        guard let selectedRange, selectedRange.length > 0,
              selectedRange.location + selectedRange.length <= (editorText as NSString).length else {
            return nil
        }
        return (editorText as NSString).substring(with: selectedRange)
    }
}

/// UITextView-backed editor that reports the current selection, so
/// transforms can target exactly the selected range.
struct MobileSelectionTextEditor: UIViewRepresentable {
    @Binding var text: String
    var onSelection: (NSRange) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeUIView(context: Context) -> UITextView {
        let view = UITextView()
        view.font = .preferredFont(forTextStyle: .body)
        view.backgroundColor = .clear
        view.textColor = UIColor(MobileTheme.textPrimary)
        view.autocorrectionType = .default
        view.delegate = context.coordinator
        return view
    }

    func updateUIView(_ view: UITextView, context: Context) {
        context.coordinator.parent = self
        if view.text != text {
            view.text = text
        }
    }

    final class Coordinator: NSObject, UITextViewDelegate {
        var parent: MobileSelectionTextEditor

        init(_ parent: MobileSelectionTextEditor) {
            self.parent = parent
        }

        func textViewDidChange(_ textView: UITextView) {
            parent.text = textView.text
        }

        func textViewDidChangeSelection(_ textView: UITextView) {
            parent.onSelection(textView.selectedRange)
        }
    }
}
