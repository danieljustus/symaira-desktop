import SwiftUI
import AppKit
import SymDeskCore
import OSLog

/// NSTextView that intercepts image pastes/drops and stores them as vault assets.
final class MarkdownTextView: NSTextView {
    /// Returns the Markdown snippet to insert for the image, or nil to fall
    /// through to default behavior.
    var onImageData: ((Data, String) -> String?)?

    private static let imageExtensions = ["png", "jpg", "jpeg", "gif", "tiff", "webp", "heic", "bmp"]

    override func paste(_ sender: Any?) {
        if insertImages(from: NSPasteboard.general) { return }
        super.paste(sender)
    }

    override func performDragOperation(_ sender: NSDraggingInfo) -> Bool {
        if insertImages(from: sender.draggingPasteboard) { return true }
        return super.performDragOperation(sender)
    }

    /// Reads image content (raw image data or image file URLs) from the
    /// pasteboard, hands it to `onImageData` and inserts the returned link.
    private func insertImages(from pasteboard: NSPasteboard) -> Bool {
        guard let handler = onImageData else { return false }

        // Image files (Finder drag or copied files)
        if let urls = pasteboard.readObjects(forClasses: [NSURL.self]) as? [URL], !urls.isEmpty {
            var snippets: [String] = []
            for url in urls {
                let ext = url.pathExtension.lowercased()
                guard Self.imageExtensions.contains(ext),
                      let data = try? Data(contentsOf: url),
                      let snippet = handler(data, ext) else { continue }
                snippets.append(snippet)
            }
            if !snippets.isEmpty {
                insertText(snippets.joined(separator: "\n"), replacementRange: selectedRange())
                return true
            }
            if pasteboard.canReadObject(forClasses: [NSURL.self], options: nil) {
                return false // non-image files: default behavior
            }
        }

        // Raw image data (e.g. screenshot in clipboard)
        for (type, ext) in [(NSPasteboard.PasteboardType.png, "png"), (.tiff, "png")] {
            guard let data = pasteboard.data(forType: type) else { continue }
            // Convert TIFF clipboard data to PNG for a portable vault asset
            var payload = data
            if type == .tiff {
                guard let rep = NSBitmapImageRep(data: data),
                      let png = rep.representation(using: .png, properties: [:]) else { continue }
                payload = png
            }
            guard let snippet = handler(payload, ext) else { continue }
            insertText(snippet, replacementRange: selectedRange())
            return true
        }
        return false
    }
}

struct MarkdownEditorView: NSViewRepresentable {
    @Binding var text: String
    var onLinkClick: ((String) -> Void)?
    /// When set, the editor offers inline AI actions (summarize/rewrite/continue)
    /// on the current selection via the right-click menu.
    var core: DeskCore?
    /// Vault root used to store pasted/dropped images under the assets folder.
    var vaultRoot: String?

    func makeCoordinator() -> Coordinator {
        Coordinator(self)
    }

    func makeNSView(context: Context) -> NSScrollView {
        let scrollView = MarkdownTextView.scrollableTextView()
        let textView = scrollView.documentView as! MarkdownTextView
        context.coordinator.textView = textView
        textView.onImageData = { [weak coordinator = context.coordinator] data, ext in
            coordinator?.storeImageAsset(data: data, ext: ext)
        }

        textView.delegate = context.coordinator
        textView.allowsUndo = true
        textView.isRichText = false
        textView.font = NSFont.monospacedSystemFont(ofSize: 14, weight: .regular)
        textView.backgroundColor = SymairaNSColors.bgDark
        textView.textColor = SymairaNSColors.textPrimary
        textView.insertionPointColor = SymairaNSColors.gold
        
        textView.textContainerInset = NSSize(width: 16, height: 16)
        
        // Disable smart quotes/dashes for markdown
        textView.isAutomaticQuoteSubstitutionEnabled = false
        textView.isAutomaticDashSubstitutionEnabled = false
        
        return scrollView
    }

    func updateNSView(_ scrollView: NSScrollView, context: Context) {
        let textView = scrollView.documentView as! NSTextView
        
        // Prevent recursive updates if the text was changed by the user typing
        if textView.string != text {
            textView.string = text
            context.coordinator.highlight(textView: textView)
        }
    }

    @MainActor
    class Coordinator: NSObject, NSTextViewDelegate {
        var parent: MarkdownEditorView
        weak var textView: NSTextView?
        private var isFormatting = false
        private var isTransforming = false
        private var highlightWorkItem: DispatchWorkItem?

        init(_ parent: MarkdownEditorView) {
            self.parent = parent
        }

        func textDidChange(_ notification: Notification) {
            guard let textView = notification.object as? NSTextView, !isFormatting else { return }
            self.parent.text = textView.string
            scheduleHighlight(textView: textView)
        }

        /// Debounce highlight calls so rapid typing does not re-highlight
        /// the entire document on every keystroke. The highlight runs at most
        /// once per 300 ms of idle time after the last text change.
        private func scheduleHighlight(textView: NSTextView) {
            highlightWorkItem?.cancel()
            let workItem = DispatchWorkItem { [weak textView] in
                guard let textView else { return }
                self.highlight(textView: textView)
            }
            highlightWorkItem = workItem
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3, execute: workItem)
        }

        // MARK: - Inline AI actions

        func textView(_ view: NSTextView, menu: NSMenu, for event: NSEvent, at charIndex: Int) -> NSMenu? {
            guard parent.core != nil, view.selectedRange().length > 0, !isTransforming else {
                return menu
            }
            let aiItem = NSMenuItem(title: "AI", action: nil, keyEquivalent: "")
            let submenu = NSMenu()
            submenu.addItem(withTitle: "Summarize selection", action: #selector(aiSummarize), keyEquivalent: "")
            submenu.addItem(withTitle: "Rewrite selection", action: #selector(aiRewrite), keyEquivalent: "")
            submenu.addItem(withTitle: "Continue from selection", action: #selector(aiContinue), keyEquivalent: "")
            for item in submenu.items { item.target = self }
            aiItem.submenu = submenu
            menu.insertItem(aiItem, at: 0)
            menu.insertItem(.separator(), at: 1)
            return menu
        }

        @objc private func aiSummarize() { runTransform(intent: "summarize", replace: true) }
        @objc private func aiRewrite() { runTransform(intent: "rewrite", replace: true) }
        @objc private func aiContinue() { runTransform(intent: "continue", replace: false) }

        /// Runs the transform on the current selection. When `replace` is true the
        /// selection is overwritten with the result; otherwise the result is
        /// inserted right after the selection (used for "continue").
        private func runTransform(intent: String, replace: Bool) {
            guard let core = parent.core, let textView, !isTransforming else { return }
            let selected = textView.selectedRange()
            guard selected.length > 0 else { return }
            let source = (textView.string as NSString).substring(with: selected)

            isTransforming = true
            let target = replace ? selected : NSRange(location: selected.location + selected.length, length: 0)

            Task { @MainActor in
                defer { self.isTransforming = false }
                var result = ""
                do {
                    for try await chunk in core.transform(text: source, intent: intent) {
                        result += chunk
                    }
                } catch {
                    result = "\n⚠️ AI-Aktion fehlgeschlagen: \(error.localizedDescription)\n"
                }
                guard !result.isEmpty else { return }
                let insertion = replace ? result : "\n" + result
                self.applyEdit(to: textView, range: target, replacement: insertion)
            }
        }

        private func applyEdit(to textView: NSTextView, range: NSRange, replacement: String) {
            guard textView.shouldChangeText(in: range, replacementString: replacement) else { return }
            textView.textStorage?.replaceCharacters(in: range, with: replacement)
            textView.didChangeText()
            parent.text = textView.string
            highlight(textView: textView)
        }

        // MARK: - Image paste/drop handling

        /// Stores pasted/dropped image data as a vault asset and returns the
        /// Markdown snippet to insert, or nil if the vault root is unavailable.
        func storeImageAsset(data: Data, ext: String) -> String? {
            guard let vaultRoot = parent.vaultRoot else {
                os_log(.error, "MarkdownEditorView: vaultRoot is nil, cannot store image")
                return nil
            }
            do {
                let relativePath = try VaultAssets.store(
                    imageData: data,
                    fileExtension: ext,
                    vaultRoot: vaultRoot
                )
                return VaultAssets.markdownLink(for: relativePath)
            } catch {
                os_log(.error, "MarkdownEditorView: failed to store image asset: %{public}@",
                       error.localizedDescription)
                return nil
            }
        }
        
        func textView(_ textView: NSTextView, clickedOnLink link: Any, at charIndex: Int) -> Bool {
            if let urlString = (link as? URL)?.absoluteString, urlString.hasPrefix("symdesk://note/") {
                let noteTitle = urlString.replacingOccurrences(of: "symdesk://note/", with: "").removingPercentEncoding ?? ""
                parent.onLinkClick?(noteTitle)
                return true
            }
            if let linkString = link as? String, linkString.hasPrefix("symdesk://note/") {
                let noteTitle = linkString.replacingOccurrences(of: "symdesk://note/", with: "").removingPercentEncoding ?? ""
                parent.onLinkClick?(noteTitle)
                return true
            }
            return false
        }

        func highlight(textView: NSTextView) {
            isFormatting = true
            defer { isFormatting = false }
            
            guard let textStorage = textView.textStorage else { return }
            let fullRange = NSRange(location: 0, length: textStorage.length)
            
            // Reset to base font
            let baseFont = NSFont.monospacedSystemFont(ofSize: 14, weight: .regular)
            textStorage.setAttributes([
                .font: baseFont,
                .foregroundColor: SymairaNSColors.textPrimary
            ], range: fullRange)
            
            let string = textStorage.string
            
            // Highlight Headers: # Header
            if let headerRegex = try? NSRegularExpression(pattern: "^#{1,6}\\s+.*$", options: .anchorsMatchLines) {
                let matches = headerRegex.matches(in: string, range: fullRange)
                for match in matches {
                    textStorage.addAttribute(.font, value: NSFont.monospacedSystemFont(ofSize: 18, weight: .bold), range: match.range)
                    textStorage.addAttribute(.foregroundColor, value: SymairaNSColors.gold, range: match.range)
                }
            }
            
            // Highlight Wikilinks: [[Link]]
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
}
