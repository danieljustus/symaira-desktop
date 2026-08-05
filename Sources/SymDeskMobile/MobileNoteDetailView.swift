import QuickLook
import SwiftUI

struct MobileNoteDetailView: View {
    @EnvironmentObject private var vault: MobileVaultStore
    let noteID: String

    @State private var mode: DetailMode = .preview
	@State private var attachmentURL: URL?
	@State private var isLoadingAttachment = false
    @State private var isEditing = false
    @State private var isChatPresented = false

    private enum DetailMode: String, CaseIterable {
        case preview = "Preview"
        case note = "Note"
    }

    private var note: MobileNote? {
        vault.notes.first { $0.id == noteID }
    }

    var body: some View {
        Group {
			if let note {
				detail(for: note)
            } else {
                ContentUnavailableView(
                    "Note unavailable",
                    systemImage: "doc.questionmark",
                    description: Text("Refresh the vault and try again.")
                )
            }
        }
        .background(MobileTheme.background)
        .navigationTitle(note?.title ?? "Note")
        .navigationBarTitleDisplayMode(.inline)
        .sheet(isPresented: $isEditing) {
            if let note {
                MobileComposerView(editingNote: note)
                    .environmentObject(vault)
            }
        }
		.task(id: noteID) {
			guard let note else { return }
			vault.recordOpened(note)
			// Donate for Handoff / Siri Suggestions so the Mac app (or a
			// second device) can pick the note up.
			MobileNoteActivity.donate(for: note)
			isLoadingAttachment = true
			attachmentURL = await vault.attachmentURL(for: note)
			isLoadingAttachment = false
			if attachmentURL == nil { mode = .note }
		}
    }

    @ViewBuilder
	private func detail(for note: MobileNote) -> some View {
        MobileBackdrop {
            VStack(spacing: 0) {
                if attachmentURL != nil {
                    Picker("Content", selection: $mode) {
                        ForEach(DetailMode.allCases, id: \.self) { mode in
                            Text(mode.rawValue).tag(mode)
                        }
                    }
                    .pickerStyle(.segmented)
                    .padding(.horizontal, 16)
                    .padding(.vertical, 12)
                }

				if isLoadingAttachment && mode == .preview {
					ProgressView("Loading original…")
						.frame(maxWidth: .infinity, maxHeight: .infinity)
				} else if mode == .preview, let attachmentURL {
                    MobileQuickLookPreview(url: attachmentURL)
                        .background(Color.black.opacity(0.2))
                        .clipShape(RoundedRectangle(cornerRadius: 20, style: .continuous))
                        .overlay {
                            RoundedRectangle(cornerRadius: 20, style: .continuous)
                                .stroke(MobileTheme.border, lineWidth: 1)
                                .allowsHitTesting(false)
                        }
                        .padding(.horizontal, 12)
                        .padding(.bottom, 10)
                } else {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 18) {
                            metadata(for: note, attachmentURL: attachmentURL)
                            MobileMarkdownView(markdown: note.body)
                        }
                        .padding(16)
                        .frame(maxWidth: 760)
                        .frame(maxWidth: .infinity)
                    }
                }
            }
        }
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
				Button {
					isEditing = true
				} label: {
					Image(systemName: "pencil")
				}
				.accessibilityLabel("Edit note")
                Button { isChatPresented = true } label: {
                    Image(systemName: "bubble.left.and.bubble.right")
                }
                .accessibilityLabel("Ask AI about this note")
            }
            ToolbarItem(placement: .topBarTrailing) {
				if let root = vault.vaultURL {
					ShareLink(item: note.fileURL(in: root)) { Image(systemName: "square.and.arrow.up") }
						.accessibilityLabel("Share note")
				} else if let attachmentURL {
					ShareLink(item: attachmentURL) { Image(systemName: "square.and.arrow.up") }
						.accessibilityLabel("Share original")
				}
            }
        }
        .sheet(isPresented: $isChatPresented) {
            // Chat with the open note as context ("summarise this").
            MobileChatView(contextNote: note)
                .environmentObject(vault)
        }
    }

    private func metadata(for note: MobileNote, attachmentURL: URL?) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: note.isDocument ? "doc.text.image" : "note.text")
                    .font(.title3)
                    .foregroundStyle(MobileTheme.gold)
                    .frame(width: 42, height: 42)
                    .background(MobileTheme.gold.opacity(0.1), in: RoundedRectangle(cornerRadius: 13))

                VStack(alignment: .leading, spacing: 4) {
                    Text(note.title)
                        .font(.title3.bold())
                        .foregroundStyle(MobileTheme.textPrimary)
                    Text(note.path)
                        .font(.caption.monospaced())
                        .foregroundStyle(MobileTheme.textMuted)
                        .lineLimit(2)
                }
                Spacer(minLength: 0)
            }

            if note.isDocument {
                Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 9) {
                    if !note.documentType.isEmpty {
                        metadataRow("Type", note.documentType.capitalized)
                    }
                    if !note.status.isEmpty {
                        metadataRow("Status", note.status.replacingOccurrences(of: "_", with: " ").capitalized)
                    }
                    if !note.documentDate.isEmpty {
                        metadataRow("Date", note.documentDate)
                    }
                    if !note.dueDate.isEmpty {
                        metadataRow("Due", note.dueDate)
                    }
                    if !note.correspondent.isEmpty {
                        metadataRow("From", note.correspondent)
                    }
                    if !note.person.isEmpty {
                        metadataRow("Person", note.person)
                    }
                    if note.asn > 0 {
                        metadataRow("ASN", String(note.asn))
                    }
                    if note.confidence > 0 {
                        metadataRow("Confidence", "\(note.confidence)%")
                    }
                    if let attachmentURL {
                        metadataRow("Original", attachmentURL.lastPathComponent)
                    }
                }
            }

            if !note.tags.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 7) {
                        ForEach(note.tags, id: \.self) { tag in
                            Text(tag)
                                .font(.caption.weight(.medium))
                                .foregroundStyle(MobileTheme.goldSoft)
                                .padding(.horizontal, 10)
                                .padding(.vertical, 6)
                                .background(MobileTheme.gold.opacity(0.1), in: Capsule())
                        }
                    }
                }
            }
        }
        .padding(18)
        .mobileLiquidGlass(elevated: true)
    }

    private func metadataRow(_ label: String, _ value: String) -> some View {
        GridRow {
            Text(label)
                .font(.caption.weight(.medium))
                .foregroundStyle(MobileTheme.textMuted)
            Text(value)
                .font(.subheadline)
                .foregroundStyle(MobileTheme.textPrimary)
                .textSelection(.enabled)
        }
    }
}

private struct MobileMarkdownView: View {
    let markdown: String

    private var rendered: AttributedString {
        let source = normalizedObsidianMarkdown(markdown)
        return (try? AttributedString(
            markdown: source,
            options: AttributedString.MarkdownParsingOptions(interpretedSyntax: .full)
        )) ?? AttributedString(source)
    }

    var body: some View {
        Text(rendered)
            .font(.body)
            .foregroundStyle(MobileTheme.textPrimary)
            .lineSpacing(4)
            .textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(18)
            .mobileLiquidGlass(cornerRadius: 20)
    }

    private func normalizedObsidianMarkdown(_ source: String) -> String {
        var output = source
        let replacements = [
            (#"!\[\[([^\]|]+)(?:\|[^\]]+)?\]\]"#, "**Attachment:** $1"),
            (#"\[\[([^\]|]+)\|([^\]]+)\]\]"#, "$2"),
            (#"\[\[([^\]]+)\]\]"#, "$1")
        ]

        for (pattern, template) in replacements {
            guard let regex = try? NSRegularExpression(pattern: pattern) else { continue }
            let range = NSRange(output.startIndex..<output.endIndex, in: output)
            output = regex.stringByReplacingMatches(in: output, range: range, withTemplate: template)
        }
        return output
    }
}

private struct MobileQuickLookPreview: UIViewControllerRepresentable {
    let url: URL

    func makeCoordinator() -> Coordinator {
        Coordinator(url: url)
    }

    func makeUIViewController(context: Context) -> QLPreviewController {
        let controller = QLPreviewController()
        controller.dataSource = context.coordinator
        return controller
    }

    func updateUIViewController(_ controller: QLPreviewController, context: Context) {
        guard context.coordinator.url != url else { return }
        context.coordinator.url = url
        controller.reloadData()
    }

    final class Coordinator: NSObject, QLPreviewControllerDataSource {
        var url: URL

        init(url: URL) {
            self.url = url
        }

        func numberOfPreviewItems(in controller: QLPreviewController) -> Int {
            1
        }

        func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> any QLPreviewItem {
            url as NSURL
        }
    }
}
