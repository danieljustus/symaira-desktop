import SwiftUI
import SymairaTheme
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
                            SymairaNotice(message: errorMessage, tone: .critical)
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
            SymairaIconView(model.isOnDevice ? .cpu : .server, size: 14)
                .foregroundStyle(SymairaTokens.ink2)
            Text(model.activeProviderName ?? "No AI provider")
                .font(SymairaTypography.label)
                .foregroundStyle(SymairaTokens.ink2)
            Spacer()
            if model.phase == .streaming {
                ProgressView()
                    .controlSize(.small)
                    .accessibilityLabel("Generating suggestion")
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 7)
        .background(SymairaTokens.surface, in: Capsule())
        .accessibilityElement(children: .combine)
    }

    private var intentPicker: some View {
        SymairaSegmentedControl(
            "Intent",
            selection: $intent,
            options: MobileInlineAIIntent.allCases.map { (value: $0, label: $0.displayName) }
        )
        .disabled(model.phase == .streaming)
    }

    private var sourceEditor: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("Text to transform")
                    .font(SymairaTypography.label)
                    .foregroundStyle(SymairaTokens.ink3)
                Spacer()
                if let selectedRange, selectedRange.length > 0 {
                    Label("\(selectedRange.length) chars selected", symairaIcon: .edit)
                        .font(SymairaTypography.caption)
                        .foregroundStyle(SymairaTokens.ink)
                    Button {
                        self.selectedRange = nil
                    } label: {
                        SymairaIconView(.close, size: 14)
                            .foregroundStyle(SymairaTokens.ink3)
                            .frame(minWidth: SymairaMetrics.minimumControlHeight, minHeight: SymairaMetrics.minimumControlHeight)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Use whole text")
                } else {
                    Text("Whole text")
                        .symairaText(.caption)
                }
            }
            MobileSelectionTextEditor(
                text: $editorText,
                onSelection: { selectedRange = $0 }
            )
            .frame(minHeight: 120, maxHeight: 220)
            .padding(8)
            .background(SymairaTokens.surface, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay {
                if editorText.isEmpty {
                    Text("Select text to transform, or leave empty for the whole text…")
                        .font(SymairaTypography.callout)
                        .foregroundStyle(SymairaTokens.ink3)
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
            Label("\(intent.displayName) text", symairaIcon: intent.symairaIcon)
                .frame(maxWidth: .infinity)
        }
        .symairaButtonStyle(.secondary)
        .disabled(
            model.phase == .streaming
                || editorText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        )
    }

    private var comparison: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Original")
                .font(SymairaTypography.label)
                .foregroundStyle(SymairaTokens.ink3)
            Text(model.transformedSource)
                .font(SymairaTypography.caption)
                .foregroundStyle(SymairaTokens.ink2)
                .lineLimit(6)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(12)
                .background(SymairaTokens.surface, in: RoundedRectangle(cornerRadius: 12, style: .continuous))

            Text("Suggestion")
                .font(SymairaTypography.label)
                .foregroundStyle(SymairaTokens.ink)
            Text(model.suggestion)
                .symairaText(.callout)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(12)
                .background(SymairaTokens.raised, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
    }

    private var acceptRow: some View {
        HStack(spacing: 10) {
            if model.accepted {
                Button(role: .destructive) {
                    Task { await model.undo() }
                } label: {
                    Label("Undo", symairaIcon: .undo)
                        .frame(maxWidth: .infinity)
                }
                .symairaButtonStyle(.secondary)
                .disabled(model.phase == .streaming)
            } else {
                Button {
                    Task { await model.accept(currentText: editorText, selectedRange: selectedRange) }
                } label: {
                    Label("Accept & save", symairaIcon: .confirm)
                        .frame(maxWidth: .infinity)
                }
                .symairaButtonStyle(.primary)
                .disabled(!model.hasResult || model.phase == .streaming || model.accepted)
            }

            Button(role: .cancel) {
                model.discardResult()
            } label: {
                Text("Discard")
                    .frame(maxWidth: .infinity)
            }
            .symairaButtonStyle(.secondary)
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

extension MobileInlineAIIntent {
    /// Icon for the intent; the model file stays free of UI types.
    var symairaIcon: SymairaIcon {
        switch self {
        case .summarize: return .quote
        case .rewrite: return .sync
        case .continue: return .add
        }
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
        view.textColor = UIColor(SymairaTokens.ink)
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
