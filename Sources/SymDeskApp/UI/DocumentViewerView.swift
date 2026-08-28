import SwiftUI
import PDFKit
import QuickLookUI
import SymairaTheme
import SymDeskCore
import SymairaIngestContract

// MARK: - PDFKit View Wrapper

struct PDFKitView: NSViewRepresentable {
    let url: URL
    @Binding var currentPageIndex: Int
    @Binding var pageCount: Int
    var zoomScale: CGFloat

    func makeNSView(context: Context) -> PDFView {
        let pdfView = PDFView()
        pdfView.autoScales = true
        pdfView.displayMode = .singlePageContinuous
        pdfView.displayDirection = .vertical
        pdfView.backgroundColor = SymairaNSColors.bgDark

        if let document = PDFDocument(url: url) {
            pdfView.document = document
            pageCount = document.pageCount
        }

        return pdfView
    }

    func updateNSView(_ pdfView: PDFView, context: Context) {
        guard let document = pdfView.document else { return }

        if document.pageCount != pageCount {
            pageCount = document.pageCount
        }

        if let currentPage = pdfView.currentPage,
           document.index(for: currentPage) != currentPageIndex,
           let page = document.page(at: currentPageIndex) {
            pdfView.go(to: page)
        }

        if zoomScale > 0 {
            pdfView.scaleFactor = zoomScale
        }
    }
}

// MARK: - Text Content View

struct TextContentView: NSViewRepresentable {
    let text: String

    func makeNSView(context: Context) -> NSScrollView {
        let scrollView = NSTextView.scrollableTextView()
        guard let textView = scrollView.documentView as? NSTextView else {
            return scrollView
        }

        textView.isEditable = false
        textView.isSelectable = true
        textView.font = NSFont.monospacedSystemFont(ofSize: 14, weight: .regular)
        textView.backgroundColor = SymairaNSColors.bgDark
        textView.textColor = SymairaNSColors.textPrimary
        textView.textContainerInset = NSSize(width: 16, height: 16)
        textView.string = text

        highlightMarkdown(textView: textView)

        return scrollView
    }

    func updateNSView(_ scrollView: NSScrollView, context: Context) {
        guard let textView = scrollView.documentView as? NSTextView else { return }
        if textView.string != text {
            textView.string = text
            highlightMarkdown(textView: textView)
        }
    }

    private func highlightMarkdown(textView: NSTextView) {
        guard let textStorage = textView.textStorage else { return }
        let fullRange = NSRange(location: 0, length: textStorage.length)

        let baseFont = NSFont.monospacedSystemFont(ofSize: 14, weight: .regular)
        textStorage.setAttributes([
            .font: baseFont,
            .foregroundColor: SymairaNSColors.textPrimary
        ], range: fullRange)

        let string = textStorage.string

        if let headerRegex = try? NSRegularExpression(pattern: "^#{1,6}\\s+.*$", options: .anchorsMatchLines) {
            let matches = headerRegex.matches(in: string, range: fullRange)
            for match in matches {
                textStorage.addAttribute(.font, value: NSFont.monospacedSystemFont(ofSize: 18, weight: .bold), range: match.range)
                textStorage.addAttribute(.foregroundColor, value: SymairaNSColors.gold, range: match.range)
            }
        }

        if let linkRegex = try? NSRegularExpression(pattern: "\\[\\[(.*?)\\]\\]") {
            let matches = linkRegex.matches(in: string, range: fullRange)
            for match in matches {
                let innerRange = match.range(at: 1)
                if let innerString = (string as NSString).substring(with: innerRange).addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) {
                    textStorage.addAttribute(.link, value: "symdesk://note/\(innerString)", range: match.range)
                    textStorage.addAttribute(.foregroundColor, value: SymairaNSColors.goldSecondary, range: match.range)
                    textStorage.addAttribute(.underlineStyle, value: NSUnderlineStyle.single.rawValue, range: match.range)
                }
            }
        }
    }
}

// MARK: - Quick Look View Wrapper

/// Native preview for archived images, Office documents, and other formats
/// supported by macOS. PDFs keep using PDFKit for page and zoom controls.
struct QuickLookPreviewView: NSViewRepresentable {
    let url: URL

    func makeNSView(context: Context) -> QLPreviewView {
        let previewView: QLPreviewView = QLPreviewView(frame: .zero, style: .normal)
        previewView.autostarts = true
        previewView.previewItem = url as NSURL
        return previewView
    }

    func updateNSView(_ previewView: QLPreviewView, context: Context) {
        let currentURL = previewView.previewItem as? URL
        if currentURL != url {
            previewView.previewItem = url as NSURL
            previewView.refreshPreviewItem()
        }
    }
}

// MARK: - Document Viewer

struct DocumentViewerView: View {
    let document: DocumentItem
    /// Opens the document in the note editor (issue #648).
    var onOpenInEditor: ((DocumentItem) -> Void)? = nil
    /// Search location that should be visible when the viewer opens.
    var initialAnchor: SearchAnchor? = nil
    /// Complete ordered search set; used for next/previous hit navigation.
    var searchHits: [SearchResult] = []
    var onNavigateToSearchHit: ((SearchResult) -> Void)? = nil

    @EnvironmentObject var core: DeskCore
    @Environment(\.dismiss) private var dismiss

    @State private var isPDFMode = true
    @State private var showInspector = false
    @State private var currentPageIndex = 0
    @State private var pageCount = 0
    @State private var zoomScale: CGFloat = 1.0
    @State private var noteContent = ""
    @State private var props: [String: String] = [:]
    @State private var noteURL: URL?
    @State private var fileURL: URL?
    @State private var isLoadingDocument = true
    @State private var loadMessage: String?
    @State private var inspectorTab: InspectorTab = .info

    @State private var editStatus: String = ""
    @State private var editDueDate: String = ""
    @State private var editType: String = ""
    @State private var editTitle: String = ""
    @State private var editCorrespondent: String = ""
    @State private var editDocumentDate: String = ""
    @State private var editPerson: String = ""
    @State private var editASN: String = ""
    @State private var editTags: String = ""
    @State private var editNoteVisible: Bool = true
    @State private var isSaving = false
    @State private var isReOCRAvailable = false
    @State private var isReOCRRunning = false
    @State private var reOCRStatus: String? = nil

    enum InspectorTab: String, CaseIterable {
        case info = "Info"
        case workflow = "Workflow"
        case system = "System"
    }

    var body: some View {
        HSplitView {
            mainContent
            if showInspector {
                inspectorPanel
                    .frame(minWidth: 280, idealWidth: 320, maxWidth: 400)
                    .symDeskLiquidGlass(cornerRadius: 0)
            }
        }
        .background(SymairaTheme.bgDark)
        .navigationTitle(document.title)
        .safeAreaInset(edge: .top, spacing: 0) {
            viewerHeader
        }
        .task(id: viewerLoadID) { await loadDocument() }
        .task { checkReOCRAvailability() }
        .background(
            KeyboardHandler(
                onSpace: { isPDFMode.toggle() },
                onCmdI: { showInspector.toggle() }
            )
        )
    }

    private var viewerLoadID: String {
        document.id + "|" + (initialAnchor?.kind ?? "") + "|" + (initialAnchor?.value ?? "")
    }

    private var currentHitIndex: Int? {
        searchHits.firstIndex { $0.path == document.path && $0.anchor == initialAnchor }
    }

    // MARK: - Main Content

    private var viewerHeader: some View {
        HStack(spacing: 10) {
            Image(systemName: docTypeIcon)
                .foregroundStyle(SymairaTheme.goldPrimary)
                .symairaText(.heading)
            VStack(alignment: .leading, spacing: 1) {
                Text(document.title)
                    .symairaText(.subheading)
                    .foregroundStyle(SymairaTheme.textPrimary)
                    .lineLimit(1)
                Text(viewerSubtitle)
                    .symairaText(.caption)
                    .foregroundStyle(SymairaTheme.textSecondary)
            }
            Spacer(minLength: 16)

            Button(action: { isPDFMode.toggle() }) {
                Label(isPDFMode ? "Text" : "Preview", systemImage: isPDFMode ? "doc.text" : "doc.richtext")
            }
            .help("Toggle PDF/Text view (Space)")

            if !searchHits.isEmpty {
                viewerDivider
                Button(action: goToPreviousHit) { Image(systemName: "arrow.up.to.line") }
                    .disabled(currentHitIndex == nil || currentHitIndex == 0)
                    .help("Previous search hit")
                Button(action: goToNextHit) { Image(systemName: "arrow.down.to.line") }
                    .disabled(currentHitIndex == nil || currentHitIndex == searchHits.count - 1)
                    .help("Next search hit")
                if let currentHitIndex {
                    Text("Hit \(currentHitIndex + 1) / \(searchHits.count)")
                        .symairaText(.caption).monospacedDigit()
                        .foregroundStyle(SymairaTheme.textSecondary)
                }
            }

            if isPDFMode, fileURL?.pathExtension.lowercased() == "pdf", pageCount > 0 {
                viewerDivider
                Button(action: { goToPreviousPage() }) {
                    Image(systemName: "chevron.left")
                }
                .disabled(currentPageIndex <= 0)
                .help("Previous page")
                Text("\(currentPageIndex + 1) / \(pageCount)")
                    .symairaText(.caption).monospacedDigit()
                    .foregroundStyle(SymairaTheme.textSecondary)
                Button(action: { goToNextPage() }) {
                    Image(systemName: "chevron.right")
                }
                .disabled(currentPageIndex >= pageCount - 1)
                .help("Next page")
            }

            if isPDFMode, fileURL?.pathExtension.lowercased() == "pdf" {
                viewerDivider
                Button(action: { zoomOut() }) { Image(systemName: "minus.magnifyingglass") }
                    .help("Zoom out")
                Button(action: { zoomFit() }) { Image(systemName: "arrow.up.left.and.arrow.down.right") }
                    .help("Zoom to fit")
                Button(action: { zoomIn() }) { Image(systemName: "plus.magnifyingglass") }
                    .help("Zoom in")
            }

            viewerDivider
            Button(action: { showInspector.toggle() }) {
                Label("Inspector", systemImage: "sidebar.right")
            }
            .help("Toggle inspector (Cmd+I)")

            if document.path.lowercased().hasSuffix(".md") {
                Button(action: {
                    onOpenInEditor?(document)
                    dismiss()
                }) {
                    Label("Open in Editor", systemImage: "pencil.and.outline")
                }
                .help("Edit this note in the vault editor")
            }

            // Re-run OCR only makes sense with an archived original to
            // re-process; a hand-written Markdown note has none (issue #648).
            if isReOCRAvailable, fileURL != nil {
                Button(action: { runReOCR() }) {
                    if isReOCRRunning {
                        ProgressView().controlSize(.small)
                    } else {
                        Label("Re-run OCR", systemImage: "doc.badge.gearshape")
                    }
                }
                .disabled(isReOCRRunning)
                .help("Re-run OCR on the archived original")
            }

            Button(action: { dismiss() }) {
                Image(systemName: "xmark")
            }
            .buttonStyle(.bordered)
            .help("Close document")
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .symDeskLiquidGlass(cornerRadius: 0, prominence: .elevated)
        .accessibilityElement(children: .contain)
    }

    private var viewerDivider: some View {
        Rectangle()
            .fill(SymairaTheme.borderGlassHover)
            .frame(width: 1, height: 20)
    }

    @ViewBuilder
    private var mainContent: some View {
        VStack(spacing: 0) {
            if isLoadingDocument {
                ProgressView("Loading preview…")
                    .tint(SymairaTheme.goldPrimary)
                    .foregroundStyle(SymairaTheme.textSecondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if isPDFMode {
                if let url = fileURL, url.pathExtension.lowercased() == "pdf" {
                    PDFKitView(
                        url: url,
                        currentPageIndex: $currentPageIndex,
                        pageCount: $pageCount,
                        zoomScale: zoomScale
                    )
                } else if let url = fileURL {
                    QuickLookPreviewView(url: url)
                } else if !noteContent.isEmpty {
                    VStack(spacing: 0) {
                        if let loadMessage {
                            Label(loadMessage, systemImage: "exclamationmark.triangle.fill")
                                .symairaText(.caption)
                                .foregroundStyle(SymairaTheme.goldSecondary)
                                .padding(.horizontal, 12)
                                .padding(.vertical, 8)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(SymairaTheme.goldPrimary.opacity(0.08))
                        }
                        MarkdownPreviewView(
                            text: noteContent,
                            resolveNote: { _ in nil },
                            visited: [document.title]
                        )
                        .padding(.horizontal, 20)
                        .frame(maxWidth: 840)
                        .frame(maxWidth: .infinity)
                    }
                } else {
                    noContentView
                }
            } else {
                if noteContent.isEmpty {
                    noContentView
                } else {
                    TextContentView(text: noteContent)
                }
            }
        }
    }

    private var noContentView: some View {
        VStack(spacing: 12) {
            Image(systemName: "doc.text.magnifyingglass")
                .symairaText(.display)
                .foregroundColor(SymairaTheme.textMuted)
            Text("No preview available")
                .symairaText(.heading)
                .foregroundColor(SymairaTheme.textSecondary)
            Text(loadMessage ?? "The original file and its Markdown note could not be read.")
                .symairaText(.callout)
                .foregroundStyle(SymairaTheme.textMuted)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 420)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    // MARK: - Inspector

    private var inspectorPanel: some View {
        VStack(spacing: 0) {
            Picker("Tab", selection: $inspectorTab) {
                ForEach(InspectorTab.allCases, id: \.self) { tab in
                    Text(tab.rawValue).tag(tab)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .padding(8)

            Divider()

            ScrollView {
                switch inspectorTab {
                case .info:
                    infoTab
                case .workflow:
                    workflowTab
                case .system:
                    systemTab
                }
            }
            .padding(12)

            Divider()

            HStack {
                Spacer()
                if isSaving {
                    ProgressView()
                        .controlSize(.small)
                }
                Button("Save Changes") { saveChanges() }
                    .buttonStyle(SymairaPrimaryButtonStyle())
                    .disabled(isSaving || !hasChanges)
                    .keyboardShortcut("s", modifiers: .command)
            }
            .padding(8)
        }
    }

    private var infoTab: some View {
        VStack(alignment: .leading, spacing: 16) {
            editableRow(label: "Title", text: $editTitle)
            editableRow(label: "Type", text: $editType)
            editableRow(label: "Document Date", text: $editDocumentDate, placeholder: "YYYY-MM-DD")
            editableRow(label: "Person", text: $editPerson)
            editableRow(label: "Correspondent", text: $editCorrespondent)
            editableRow(label: "Archive Serial Number", text: $editASN, placeholder: "ASN (or \"next\")", monospaced: true)

            VStack(alignment: .leading, spacing: 4) {
                Text("Tags")
                    .symairaText(.caption)
                    .foregroundColor(.secondary)
                TextField("Tags (comma-separated)", text: $editTags)
                    .textFieldStyle(.symaira)
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("Note")
                    .symairaText(.caption)
                    .foregroundColor(.secondary)
                Toggle("Visible in note", isOn: $editNoteVisible)
                    .toggleStyle(.checkbox)
            }

            if document.confidence > 0 {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Classification Confidence")
                        .symairaText(.caption)
                        .foregroundColor(.secondary)
                    HStack {
                        confidenceBar
                        Text("\(document.confidence)%")
                            .symairaText(.caption).monospacedDigit()
                            .foregroundColor(.secondary)
                    }
                }
            }
        }
    }

    private var workflowTab: some View {
        VStack(alignment: .leading, spacing: 16) {
            VStack(alignment: .leading, spacing: 8) {
                Text("Status")
                    .symairaText(.caption)
                    .foregroundColor(.secondary)
                FlowLayout(spacing: 6) {
                    ForEach(DocumentStatus.allCases) { status in
                        statusChip(status: status)
                    }
                }
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("Due Date")
                    .symairaText(.caption)
                    .foregroundColor(.secondary)
                TextField("YYYY-MM-DD", text: $editDueDate)
                    .textFieldStyle(.symaira)
            }
        }
    }

    private func statusChip(status: DocumentStatus) -> some View {
        Button(action: { editStatus = status.rawValue }) {
            HStack(spacing: 4) {
                Image(systemName: status.systemImage)
                    .symairaText(.caption)
                Text(status.label)
                    .symairaText(.caption)
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(editStatus == status.rawValue ? statusColor(for: status).opacity(0.2) : Color.white.opacity(0.05))
            .foregroundColor(editStatus == status.rawValue ? statusColor(for: status) : SymairaTheme.textSecondary)
            .cornerRadius(6)
            .overlay(
                RoundedRectangle(cornerRadius: 6)
                    .stroke(editStatus == status.rawValue ? statusColor(for: status) : Color.clear, lineWidth: 1)
            )
        }
        .buttonStyle(.plain)
    }

    private func statusColor(for status: DocumentStatus) -> Color {
        symairaStatusColor(status)
    }

    private var systemTab: some View {
        VStack(alignment: .leading, spacing: 12) {
            inspectorRow(label: "Note Path", value: noteURL?.path ?? document.path, isMonospaced: true)

            if let url = fileURL {
                inspectorRow(label: "Original", value: url.path, isMonospaced: true)
                if let attrs = try? FileManager.default.attributesOfItem(atPath: url.path),
                   let size = attrs[.size] as? Int64 {
                    inspectorRow(label: "Size", value: ByteCountFormatter.string(fromByteCount: size, countStyle: .file))
                }
                inspectorRow(label: "Extension", value: url.pathExtension.uppercased())
            }

            if !props.isEmpty {
                Divider()
                Text("Properties")
                    .symairaText(.caption)
                    .foregroundColor(.secondary)
                ForEach(props.sorted(by: { $0.key < $1.key }), id: \.key) { key, value in
                    inspectorRow(label: key, value: value, isMonospaced: true)
                }
            }

            if let status = reOCRStatus {
                Divider()
                HStack(alignment: .top, spacing: 4) {
                    Image(systemName: "doc.badge.gearshape")
                        .foregroundColor(SymairaTheme.goldSecondary)
                    Text(status)
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textSecondary)
                        .lineLimit(nil)
                }
            }
        }
    }

    // MARK: - Helpers

    private func inspectorRow(label: String, value: String, isMonospaced: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label)
                .symairaText(.caption)
                .foregroundColor(.secondary)
            if value.isEmpty {
                Text("—")
                    .symairaText(isMonospaced ? .monoSmall : .body).monospacedDigit()
                    .foregroundColor(.secondary)
            } else {
                Text(value)
                    .symairaText(isMonospaced ? .monoSmall : .body).monospacedDigit()
                    .textSelection(.enabled)
            }
        }
    }

    private func editableRow(label: String, text: Binding<String>, placeholder: String = "", monospaced: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .symairaText(.caption)
                .foregroundColor(.secondary)
            TextField(placeholder, text: text)
                .textFieldStyle(.symaira)
                .symairaText(monospaced ? .mono : .body)
        }
    }

    private var confidenceBar: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2)
                    .fill(Color.white.opacity(0.08))
                    .frame(height: 6)
                RoundedRectangle(cornerRadius: 2)
                    .fill(confidenceColor)
                    .frame(width: max(0, min(geo.size.width, geo.size.width * CGFloat(document.confidence) / 100.0)), height: 6)
            }
        }
        .frame(height: 6)
    }

    private var confidenceColor: Color {
        if document.confidence >= 80 { return SymairaTheme.goldPrimary }
        if document.confidence >= 50 { return .orange }
        return .red
    }

    private var docTypeIcon: String {
        switch document.documentType.lowercased() {
        case "invoice": return "dollarsign.circle"
        case "receipt": return "receipt"
        case "contract": return "doc.plaintext"
        case "letter": return "envelope"
        case "tax": return "chart.bar.docpath"
        case "insurance": return "shield"
        default: return "doc.text"
        }
    }

    private var hasChanges: Bool {
        editStatus != document.status
            || editDueDate != document.dueDate
            || editType != document.documentType
            || editTitle != document.title
            || editCorrespondent != document.correspondent
            || editDocumentDate != document.documentDate
            || editPerson != document.person
            || editASN != (document.asn > 0 ? "\(document.asn)" : "")
            || editTags != (props["tags"] ?? "")
            || editNoteVisible != (props["note_visible"] != "false")
    }

    private var viewerSubtitle: String {
        if isLoadingDocument { return "Loading preview…" }
        if let initialAnchor { return initialAnchor.displayValue }
        if !isPDFMode { return "Extracted text" }
        if let fileURL { return fileURL.lastPathComponent }
        return "Rendered note"
    }

    // MARK: - Actions

    private func loadDocument() async {
        isLoadingDocument = true
        loadMessage = nil
        fileURL = nil
        noteURL = DocumentPreviewResolver.noteURL(
            documentPath: document.path,
            vaultPath: core.vaultPath
        )
        editStatus = document.status
        editDueDate = document.dueDate
        editType = document.documentType
        editTitle = document.title
        editCorrespondent = document.correspondent
        editDocumentDate = document.documentDate
        editPerson = document.person
        editASN = document.asn > 0 ? "\(document.asn)" : ""

        async let loadedProps: [String: String] = loadProperties()
        async let loadedContent: String = loadNoteContent()
        let (newProps, newContent) = await (loadedProps, loadedContent)

        guard !Task.isCancelled else { return }
        props = newProps
        noteContent = newContent
        editTags = newProps["tags"] ?? ""
        editNoteVisible = newProps["note_visible"] != "false"
        fileURL = DocumentPreviewResolver.sourceURL(
            documentPath: document.path,
            properties: newProps,
            vaultPath: core.vaultPath
        )
		if core.isRemote {
			for key in DocumentPreviewResolver.sourcePropertyKeys {
				guard let path = newProps[key], !path.isEmpty else { continue }
				do {
					fileURL = try await core.remoteCachedFile(path: path)
					break
				} catch {
					loadMessage = "The archived original could not be downloaded: \(error.localizedDescription)"
				}
			}
		}
        if fileURL == nil, !DocumentPreviewResolver.sourcePropertyKeys.allSatisfy({ newProps[$0] == nil }) {
            loadMessage = "The archived original referenced by this note was not found."
        }
        if initialAnchor?.kind == "page", let page = Int(initialAnchor?.value ?? ""), page > 0 {
            currentPageIndex = page - 1
        }
        isLoadingDocument = false
    }

    private func loadProperties() async -> [String: String] {
        do {
            return try await core.docProps(path: document.path)
        } catch {
            print("docProps failed: \(error)")
            return [:]
        }
    }

    private func loadNoteContent() async -> String {
		if core.isRemote {
			do {
				return try await core.docNoteContent(path: document.path)
			} catch {
				loadMessage = "The Markdown note could not be downloaded: \(error.localizedDescription)"
				return ""
			}
		}
        guard let noteURL else {
            loadMessage = "No vault is configured for the relative document path."
            return ""
        }
        do {
            return try await core.docNoteContent(path: noteURL.path)
        } catch {
            loadMessage = "The Markdown note could not be read: \(error.localizedDescription)"
            return ""
        }
    }

    private func goToPreviousHit() {
        guard let index = currentHitIndex, index > 0 else { return }
        onNavigateToSearchHit?(searchHits[index - 1])
    }

    private func goToNextHit() {
        guard let index = currentHitIndex, index + 1 < searchHits.count else { return }
        onNavigateToSearchHit?(searchHits[index + 1])
    }

    private func goToPreviousPage() {
        if currentPageIndex > 0 {
            currentPageIndex -= 1
        }
    }

    private func goToNextPage() {
        if currentPageIndex < pageCount - 1 {
            currentPageIndex += 1
        }
    }

    private func zoomIn() {
        zoomScale = min(zoomScale + 0.25, 5.0)
    }

    private func zoomOut() {
        zoomScale = max(zoomScale - 0.25, 0.25)
    }

    private func zoomFit() {
        zoomScale = 1.0
    }

    private func saveChanges() {
        isSaving = true
        Task {
            do {
                if editStatus != document.status {
                    try await core.docSetStatus(path: document.path, status: editStatus)
                }
                if editDueDate != document.dueDate {
                    try await core.docSetDue(path: document.path, date: editDueDate)
                }
                if editType != document.documentType {
                    try await core.docSetType(path: document.path, type: editType)
                }
                if editTitle != document.title {
                    try await core.docSetTitle(path: document.path, title: editTitle)
                }
                if editCorrespondent != document.correspondent {
                    try await core.docSetCorrespondent(path: document.path, name: editCorrespondent)
                }
                if editDocumentDate != document.documentDate {
                    try await core.docSetDocumentDate(path: document.path, date: editDocumentDate)
                }
                if editPerson != document.person {
                    try await core.docSetPerson(path: document.path, person: editPerson)
                }
                if editASN != (document.asn > 0 ? "\(document.asn)" : "") {
                    let value = editASN.isEmpty ? "0" : editASN
                    try await core.docSetASN(path: document.path, value: value)
                }
                if editTags != (props["tags"] ?? "") {
                    try await core.docSetTags(path: document.path, tags: editTags)
                }
                if editNoteVisible != (props["note_visible"] != "false") {
                    try await core.docSetNoteVisible(path: document.path, visible: editNoteVisible)
                }

                props = try await core.docProps(path: document.path)
                isSaving = false
            } catch {
                isSaving = false
                print("saveChanges failed: \(error)")
            }
        }
    }

    // Availability now follows whether the core is ready, not whether a
    // separately located `symingest` binary exists (#610) — reocr runs
    // in-process through `symdesk ingest reocr` via `core`.
    private func checkReOCRAvailability() {
        isReOCRAvailable = core.isReady
    }

    private func runReOCR() {
        guard let sourcePath = fileURL?.path ?? (document.path.isEmpty ? nil : document.path) else { return }
        Task {
            isReOCRRunning = true
            reOCRStatus = nil
            defer { isReOCRRunning = false }
            do {
                let response = try await core.reprocess(archivePath: sourcePath)
                if response.status == "completed" {
                    reOCRStatus = "Re-OCR completed (job \(response.jobID))"
                } else if response.status == "already_running" {
                    reOCRStatus = "Re-OCR already running"
                } else if let error = response.error {
                    reOCRStatus = "Re-OCR failed: \(error.message)"
                } else {
                    reOCRStatus = "Re-OCR status: \(response.status)"
                }
            } catch {
                reOCRStatus = "Re-OCR error: \(error.localizedDescription)"
            }
        }
    }
}

// MARK: - Flow Layout

struct FlowLayout: Layout {
    var spacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let result = arrange(proposal: proposal, subviews: subviews)
        return result.size
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let result = arrange(proposal: proposal, subviews: subviews)
        for (index, position) in result.positions.enumerated() {
            subviews[index].place(at: CGPoint(x: bounds.minX + position.x, y: bounds.minY + position.y), proposal: .unspecified)
        }
    }

    private func arrange(proposal: ProposedViewSize, subviews: Subviews) -> (positions: [CGPoint], size: CGSize) {
        let maxWidth = proposal.width ?? .infinity
        var positions: [CGPoint] = []
        var x: CGFloat = 0
        var y: CGFloat = 0
        var rowHeight: CGFloat = 0
        var totalHeight: CGFloat = 0

        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            if x + size.width > maxWidth, x > 0 {
                x = 0
                y += rowHeight + spacing
                rowHeight = 0
            }
            positions.append(CGPoint(x: x, y: y))
            rowHeight = max(rowHeight, size.height)
            x += size.width + spacing
            totalHeight = max(totalHeight, y + rowHeight)
        }

        return (positions, CGSize(width: maxWidth, height: totalHeight))
    }
}

// MARK: - Keyboard Handler

struct KeyboardHandler: NSViewRepresentable {
    var onSpace: () -> Void
    var onCmdI: () -> Void

    func makeNSView(context: Context) -> NSView {
        let view = KeyInterceptorView()
        view.onSpace = onSpace
        view.onCmdI = onCmdI
        return view
    }

    func updateNSView(_ nsView: NSView, context: Context) {
        guard let view = nsView as? KeyInterceptorView else { return }
        view.onSpace = onSpace
        view.onCmdI = onCmdI
    }
}

class KeyInterceptorView: NSView {
    var onSpace: (() -> Void)?
    var onCmdI: (() -> Void)?

    override var acceptsFirstResponder: Bool { true }

    override func keyDown(with event: NSEvent) {
        if event.charactersIgnoringModifiers == " " {
            onSpace?()
        } else if event.modifierFlags.contains(.command) && event.charactersIgnoringModifiers == "i" {
            onCmdI?()
        } else {
            super.keyDown(with: event)
        }
    }
}
