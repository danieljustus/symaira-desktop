import SwiftUI
import PDFKit
import SymDeskCore

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
        pdfView.backgroundColor = NSColor.textBackgroundColor

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
        textView.backgroundColor = .textBackgroundColor
        textView.textColor = .textColor
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
            .foregroundColor: NSColor.textColor
        ], range: fullRange)

        let string = textStorage.string

        if let headerRegex = try? NSRegularExpression(pattern: "^#{1,6}\\s+.*$", options: .anchorsMatchLines) {
            let matches = headerRegex.matches(in: string, range: fullRange)
            for match in matches {
                textStorage.addAttribute(.font, value: NSFont.monospacedSystemFont(ofSize: 18, weight: .bold), range: match.range)
                textStorage.addAttribute(.foregroundColor, value: NSColor.controlAccentColor, range: match.range)
            }
        }

        if let linkRegex = try? NSRegularExpression(pattern: "\\[\\[(.*?)\\]\\]") {
            let matches = linkRegex.matches(in: string, range: fullRange)
            for match in matches {
                let innerRange = match.range(at: 1)
                if let innerString = (string as NSString).substring(with: innerRange).addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) {
                    textStorage.addAttribute(.link, value: "symdesk://note/\(innerString)", range: match.range)
                    textStorage.addAttribute(.foregroundColor, value: NSColor.linkColor, range: match.range)
                    textStorage.addAttribute(.underlineStyle, value: NSUnderlineStyle.single.rawValue, range: match.range)
                }
            }
        }
    }
}

// MARK: - Document Viewer

struct DocumentViewerView: View {
    let document: DocumentItem

    @EnvironmentObject var core: DeskCore
    @Environment(\.dismiss) private var dismiss

    @State private var isPDFMode = true
    @State private var showInspector = false
    @State private var currentPageIndex = 0
    @State private var pageCount = 0
    @State private var zoomScale: CGFloat = 1.0
    @State private var noteContent = ""
    @State private var props: [String: String] = [:]
    @State private var fileURL: URL?
    @State private var inspectorTab: InspectorTab = .info

    @State private var editStatus: String = ""
    @State private var editDueDate: String = ""
    @State private var editType: String = ""
    @State private var editTags: String = ""
    @State private var editNoteVisible: Bool = true
    @State private var isSaving = false

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
            }
        }
        .navigationTitle(document.title)
        .toolbar {
            ToolbarItemGroup {
                Button(action: { isPDFMode.toggle() }) {
                    Label(isPDFMode ? "Show Text" : "Show PDF",
                          systemImage: isPDFMode ? "doc.text" : "doc.plaintext")
                }
                .help("Toggle PDF/Text view (Space)")

                Divider()

                if isPDFMode && pageCount > 0 {
                    Button(action: { goToPreviousPage() }) {
                        Image(systemName: "chevron.left")
                    }
                    .disabled(currentPageIndex <= 0)
                    .help("Previous page")

                    Text("\(currentPageIndex + 1) of \(pageCount)")
                        .font(.caption)
                        .monospacedDigit()

                    Button(action: { goToNextPage() }) {
                        Image(systemName: "chevron.right")
                    }
                    .disabled(currentPageIndex >= pageCount - 1)
                    .help("Next page")

                    Divider()
                }

                Button(action: { zoomOut() }) {
                    Image(systemName: "minus.magnifyingglass")
                }
                .help("Zoom out")

                Button(action: { zoomFit() }) {
                    Image(systemName: "1.magnifyingglass")
                }
                .help("Zoom to fit")

                Button(action: { zoomIn() }) {
                    Image(systemName: "plus.magnifyingglass")
                }
                .help("Zoom in")

                Divider()

                Button(action: { showInspector.toggle() }) {
                    Label("Inspector", systemImage: "sidebar.right")
                }
                .help("Toggle inspector (Cmd+I)")

                Button(action: { dismiss() }) {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundColor(.secondary)
                }
                .buttonStyle(.plain)
            }
        }
        .onAppear {
            loadDocument()
        }
        .background(
            KeyboardHandler(
                onSpace: { isPDFMode.toggle() },
                onCmdI: { showInspector.toggle() }
            )
        )
    }

    // MARK: - Main Content

    @ViewBuilder
    private var mainContent: some View {
        VStack(spacing: 0) {
            if isPDFMode {
                if let url = fileURL, url.pathExtension.lowercased() == "pdf" {
                    PDFKitView(
                        url: url,
                        currentPageIndex: $currentPageIndex,
                        pageCount: $pageCount,
                        zoomScale: zoomScale
                    )
                } else if let url = fileURL {
                    fallbackFileView(url: url)
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

    private func fallbackFileView(url: URL) -> some View {
        VStack(spacing: 16) {
            Image(systemName: docTypeIcon)
                .font(.system(size: 64))
                .foregroundColor(.accentColor)
            Text(document.title)
                .font(.title2)
                .fontWeight(.semibold)
            Text(url.pathExtension.uppercased())
                .font(.caption)
                .foregroundColor(.secondary)
                .padding(.horizontal, 8)
                .padding(.vertical, 4)
                .background(Color.gray.opacity(0.15))
                .cornerRadius(4)
            Button("Open in Default App") {
                NSWorkspace.shared.open(url)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var noContentView: some View {
        VStack(spacing: 12) {
            Image(systemName: "doc.text.magnifyingglass")
                .font(.system(size: 48))
                .foregroundColor(.secondary)
            Text("No preview available")
                .font(.title3)
                .foregroundColor(.secondary)
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
                    .disabled(isSaving || !hasChanges)
                    .keyboardShortcut("s", modifiers: .command)
            }
            .padding(8)
        }
    }

    private var infoTab: some View {
        VStack(alignment: .leading, spacing: 16) {
            inspectorRow(label: "Title", value: document.title)
            inspectorRow(label: "Type", value: document.documentType)
            inspectorRow(label: "Document Date", value: document.documentDate)
            inspectorRow(label: "Person", value: document.person)

            VStack(alignment: .leading, spacing: 4) {
                Text("Correspondent")
                    .font(.caption)
                    .foregroundColor(.secondary)
                if !document.correspondent.isEmpty {
                    Text("[[\(document.correspondent)]]")
                        .font(.body)
                        .foregroundColor(Color(nsColor: .linkColor))
                } else {
                    Text("—")
                        .font(.body)
                        .foregroundColor(.secondary)
                }
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("Tags")
                    .font(.caption)
                    .foregroundColor(.secondary)
                TextField("Tags (comma-separated)", text: $editTags)
                    .textFieldStyle(.roundedBorder)
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("Note")
                    .font(.caption)
                    .foregroundColor(.secondary)
                Toggle("Visible in note", isOn: $editNoteVisible)
                    .toggleStyle(.checkbox)
            }

            if document.confidence > 0 {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Classification Confidence")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    HStack {
                        confidenceBar
                        Text("\(document.confidence)%")
                            .font(.caption.monospacedDigit())
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
                    .font(.caption)
                    .foregroundColor(.secondary)
                FlowLayout(spacing: 6) {
                    ForEach(DocumentStatus.allCases) { status in
                        statusChip(status: status)
                    }
                }
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("Due Date")
                    .font(.caption)
                    .foregroundColor(.secondary)
                TextField("YYYY-MM-DD", text: $editDueDate)
                    .textFieldStyle(.roundedBorder)
            }
        }
    }

    private func statusChip(status: DocumentStatus) -> some View {
        Button(action: { editStatus = status.rawValue }) {
            HStack(spacing: 4) {
                Image(systemName: status.systemImage)
                    .font(.caption2)
                Text(status.label)
                    .font(.caption)
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(editStatus == status.rawValue ? statusColor(for: status).opacity(0.2) : Color.gray.opacity(0.1))
            .foregroundColor(editStatus == status.rawValue ? statusColor(for: status) : .primary)
            .cornerRadius(6)
            .overlay(
                RoundedRectangle(cornerRadius: 6)
                    .stroke(editStatus == status.rawValue ? statusColor(for: status) : Color.clear, lineWidth: 1)
            )
        }
        .buttonStyle(.plain)
    }

    private func statusColor(for status: DocumentStatus) -> Color {
        switch status {
        case .open: return .blue
        case .paid, .done: return .green
        case .submitted: return .purple
        case .needsReview: return .orange
        case .waitingForReply: return .yellow
        }
    }

    private var systemTab: some View {
        VStack(alignment: .leading, spacing: 12) {
            inspectorRow(label: "Path", value: document.path, isMonospaced: true)

            if let url = fileURL {
                if let attrs = try? FileManager.default.attributesOfItem(atPath: url.path),
                   let size = attrs[.size] as? Int64 {
                    inspectorRow(label: "Size", value: ByteCountFormatter.string(fromByteCount: size, countStyle: .file))
                }
                inspectorRow(label: "Extension", value: url.pathExtension.uppercased())
            }

            if !props.isEmpty {
                Divider()
                Text("Properties")
                    .font(.caption)
                    .foregroundColor(.secondary)
                ForEach(props.sorted(by: { $0.key < $1.key }), id: \.key) { key, value in
                    inspectorRow(label: key, value: value, isMonospaced: true)
                }
            }
        }
    }

    // MARK: - Helpers

    private func inspectorRow(label: String, value: String, isMonospaced: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label)
                .font(.caption)
                .foregroundColor(.secondary)
            if value.isEmpty {
                Text("—")
                    .font(isMonospaced ? .caption.monospacedDigit() : .body)
                    .foregroundColor(.secondary)
            } else {
                Text(value)
                    .font(isMonospaced ? .caption.monospacedDigit() : .body)
                    .textSelection(.enabled)
            }
        }
    }

    private var confidenceBar: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: 2)
                    .fill(Color.gray.opacity(0.15))
                    .frame(height: 6)
                RoundedRectangle(cornerRadius: 2)
                    .fill(confidenceColor)
                    .frame(width: max(0, min(geo.size.width, geo.size.width * CGFloat(document.confidence) / 100.0)), height: 6)
            }
        }
        .frame(height: 6)
    }

    private var confidenceColor: Color {
        if document.confidence >= 80 { return .green }
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
            || editTags != (props["tags"] ?? "")
            || editNoteVisible != (props["note_visible"] != "false")
    }

    // MARK: - Actions

    private func loadDocument() {
        let url = URL(fileURLWithPath: document.path)
        fileURL = url

        editStatus = document.status
        editDueDate = document.dueDate
        editType = document.documentType
        editNoteVisible = props["note_visible"] != "false"

        Task {
            do {
                noteContent = try await core.docNoteContent(path: document.path)
            } catch {
                noteContent = "Error loading content: \(error.localizedDescription)"
            }
        }

        Task {
            do {
                props = try await core.docProps(path: document.path)
                editTags = props["tags"] ?? ""
                editNoteVisible = props["note_visible"] != "false"
            } catch {
                print("docProps failed: \(error)")
            }
        }
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
