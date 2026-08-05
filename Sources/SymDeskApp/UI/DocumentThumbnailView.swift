import SwiftUI
import SymairaTheme
import SymDeskCore

// MARK: - Content Preview Thumbnail

/// Extracts a text preview from the first lines of a Markdown document,
/// stripping YAML frontmatter and showing the first meaningful content.
///
/// Falls back to the document-type icon when thumbnails are disabled in
/// Settings, while content loads, or if reading the file fails.
struct DocumentThumbnailView: View {
    @EnvironmentObject var core: DeskCore
    @AppStorage("showDocumentThumbnails") private var showThumbnails = true

    let doc: DocumentItem

    @State private var previewText: String?
    @State private var isLoading = false
    @State private var loadError: String?

    /// Simple in-memory LRU-ish cache so scrolling back to a card doesn't
    /// re-read the file from disk or re-fetch over the network.
    private static var previewCache: [String: String] = [:]
    private static let queue = DispatchQueue(label: "preview-cache")

    var body: some View {
        Group {
            if showThumbnails {
                if isLoading {
                    ProgressView()
                        .controlSize(.small)
                        .tint(SymairaTheme.goldSecondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                } else if let text = previewText, !text.isEmpty {
                    thumbnailContent(text: text)
                } else {
                    iconView
                }
            } else {
                iconView
            }
        }
        .task(id: doc.id) {
            await loadPreview()
        }
    }

    /// Styled preview block that replaces the generic icon.
    @ViewBuilder
    private func thumbnailContent(text: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(text)
                .symairaText(.caption)
                .foregroundColor(SymairaTheme.textSecondary.opacity(0.85))
                .lineLimit(4)
                .frame(maxWidth: .infinity, alignment: .leading)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 6, style: .continuous)
                .fill(SymairaTheme.bgDarker.opacity(0.5))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 6, style: .continuous)
                .stroke(SymairaTheme.borderGlass.opacity(0.4), lineWidth: 1)
        )
    }

    /// Original type-based SF Symbol icon used when thumbnails are off.
    private var iconView: some View {
        Image(systemName: docTypeIcon)
            .symairaText(.heading)
            .foregroundColor(SymairaTheme.goldPrimary)
    }

    // MARK: - Preview Loading

    private func loadPreview() async {
        // Check cache first
        if let cached = Self.readCache(for: doc.path) {
            previewText = cached
            return
        }

        isLoading = true
        defer { isLoading = false }

        do {
            let content: String
            if core.isRemote {
                content = try await core.docNoteContent(path: doc.path)
            } else {
                guard let url = DocumentPreviewResolver.noteURL(
                    documentPath: doc.path,
                    vaultPath: core.vaultPath
                ) else {
                    loadError = "Could not resolve path"
                    return
                }
                content = try String(contentsOf: url, encoding: .utf8)
            }

            let preview = Self.extractPreview(from: content)
            Self.writeCache(preview, for: doc.path)
            previewText = preview
        } catch {
            loadError = error.localizedDescription
        }
    }

    /// Strips YAML frontmatter and returns the first ~200 characters of body
    /// content, preferring the first heading when present.
    static func extractPreview(from content: String, maxLength: Int = 200) -> String {
        // Strip YAML frontmatter delimited by --- … ---
        var body = content
        if body.hasPrefix("---") {
            if let end = body.dropFirst(3).firstRange(of: "\n---") {
                body = String(body[end.upperBound...]).trimmingCharacters(in: .newlines)
            }
        }

        // Try to find the first heading (# …)
        for line in body.split(separator: "\n", omittingEmptySubsequences: true) {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.hasPrefix("# ") || trimmed.hasPrefix("## ") || trimmed.hasPrefix("### ") {
                let heading = trimmed.replacingOccurrences(of: #"^#{1,6}\s+"#, with: "", options: .regularExpression)
                if heading.count <= maxLength {
                    return String(heading)
                }
                break
            }
        }

        // Fall back to the first few non-empty lines of content.
        var preview = ""
        for line in body.split(separator: "\n", omittingEmptySubsequences: true) {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty else { continue }
            // Skip things that look like list items, blockquotes or
            // horizontal rules that make poor thumbnails.
            if trimmed == "---" || trimmed == "***" || trimmed == "___" { continue }

            let remaining = maxLength - preview.count
            guard remaining > 0 else { break }

            if trimmed.count > remaining {
                preview += trimmed.prefix(remaining - 1) + "…"
            } else {
                preview += trimmed + "\n"
            }

            if preview.count >= maxLength { break }
        }

        return preview.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    // MARK: - Cache

    private static func readCache(for path: String) -> String? {
        queue.sync { previewCache[path] }
    }

    private static func writeCache(_ text: String, for path: String) {
        queue.sync { previewCache[path] = text }
    }

    // MARK: - Icon helper

    private var docTypeIcon: String {
        switch doc.documentType.lowercased() {
        case "invoice": return "dollarsign.circle"
        case "receipt": return "receipt"
        case "contract": return "doc.plaintext"
        case "letter": return "envelope"
        case "tax": return "chart.bar.docpath"
        case "insurance": return "shield"
        default: return "doc.text"
        }
    }
}
