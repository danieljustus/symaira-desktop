import SwiftUI
import AppKit
import UniformTypeIdentifiers

struct PreviewView: View {
    @State private var markdownPath = ""
    @State private var originalPath = ""
    @State private var frontmatter = ""
    @State private var bodyText = ""
    @State private var errorMessage: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Document Preview")
                        .font(Font.title.bold())
                        .foregroundStyle(Theme.textPrimary)
                    Text("Inspect generated Markdown frontmatter/body and reveal the archived original without editing content.")
                        .font(.subheadline)
                        .foregroundStyle(Theme.textSecondary)
                }
                Spacer()
            }

            HStack(spacing: 12) {
                TextField("Generated Markdown note", text: $markdownPath)
                    .textFieldStyle(.roundedBorder)
                Button("Choose Note") { chooseMarkdown() }
                    .buttonStyle(.bordered)
                Button("Reload") { loadMarkdown() }
                    .buttonStyle(.borderedProminent)
                    .tint(Theme.goldPrimary)
                    .disabled(markdownPath.isEmpty)
            }

            if let errorMessage {
                Text(errorMessage)
                    .font(.caption)
                    .foregroundStyle(.red)
            }

            HStack(alignment: .top, spacing: 16) {
                VStack(alignment: .leading, spacing: 8) {
                    Text("Frontmatter")
                        .font(.headline)
                        .foregroundStyle(Theme.goldPrimary)
                    ScrollView {
                        Text(frontmatter.isEmpty ? "No note loaded." : frontmatter)
                            .font(.caption.monospaced())
                            .foregroundStyle(Theme.textSecondary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .textSelection(.enabled)
                    }
                    .padding(10)
                    .background(Color.black.opacity(0.45))
                    .clipShape(RoundedRectangle(cornerRadius: 8))
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)

                VStack(alignment: .leading, spacing: 8) {
                    HStack {
                        Text("Markdown Body")
                            .font(.headline)
                            .foregroundStyle(Theme.goldPrimary)
                        Spacer()
                        Button("Reveal Original") {
                            reveal(path: originalPath)
                        }
                        .disabled(originalPath.isEmpty)
                    }
                    ScrollView {
                        Text(bodyText.isEmpty ? "No body loaded." : bodyText)
                            .font(.body.monospaced())
                            .foregroundStyle(Theme.textPrimary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .textSelection(.enabled)
                    }
                    .padding(10)
                    .background(Color.black.opacity(0.45))
                    .clipShape(RoundedRectangle(cornerRadius: 8))
                    if !originalPath.isEmpty {
                        Text("Original: \(originalPath)")
                            .font(.caption.monospaced())
                            .foregroundStyle(Theme.textMuted)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
    }

    private func chooseMarkdown() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = false
        panel.canChooseFiles = true
        panel.allowedContentTypes = [UTType(filenameExtension: "md"), UTType.plainText].compactMap { $0 }
        if panel.runModal() == .OK, let url = panel.url {
            markdownPath = url.path
            loadMarkdown()
        }
    }

    private func loadMarkdown() {
        errorMessage = nil
        frontmatter = ""
        bodyText = ""
        originalPath = ""
        do {
            let raw = try String(contentsOfFile: markdownPath, encoding: .utf8)
            let parts = splitFrontmatter(raw)
            frontmatter = parts.frontmatter
            bodyText = parts.body
            originalPath = extractArchivePath(from: parts.frontmatter)
        } catch {
            errorMessage = "Failed to load note: \(error.localizedDescription)"
        }
    }

    private func splitFrontmatter(_ raw: String) -> (frontmatter: String, body: String) {
        guard raw.hasPrefix("---\n") else { return ("", raw) }
        let rest = String(raw.dropFirst(4))
        guard let range = rest.range(of: "\n---") else { return ("", raw) }
        let fm = String(rest[..<range.lowerBound])
        var body = String(rest[range.upperBound...])
        if body.hasPrefix("\n") { body.removeFirst() }
        return (fm, body)
    }

    private func extractArchivePath(from yaml: String) -> String {
        for line in yaml.components(separatedBy: .newlines) {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.hasPrefix("archive_path:") {
                return String(trimmed.dropFirst("archive_path:".count)).trimmingCharacters(in: .whitespacesAndNewlines).trimmingCharacters(in: CharacterSet(charactersIn: "\""))
            }
        }
        return ""
    }

    private func reveal(path: String) {
        guard !path.isEmpty else { return }
        NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)])
    }
}
