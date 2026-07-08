import SwiftUI
import AppKit
import SymDeskCore

struct MarkdownEditorView: NSViewRepresentable {
    @Binding var text: String
    var onLinkClick: ((String) -> Void)?
    /// When set, the editor offers inline AI actions (summarize/rewrite/continue)
    /// on the current selection via the right-click menu.
    var core: DeskCore?

    func makeCoordinator() -> Coordinator {
        Coordinator(self)
    }

    func makeNSView(context: Context) -> NSScrollView {
        let scrollView = NSTextView.scrollableTextView()
        let textView = scrollView.documentView as! NSTextView
        context.coordinator.textView = textView

        textView.delegate = context.coordinator
        textView.allowsUndo = true
        textView.isRichText = false
        textView.font = NSFont.monospacedSystemFont(ofSize: 14, weight: .regular)
        textView.backgroundColor = .textBackgroundColor
        textView.textColor = .textColor
        
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

        init(_ parent: MarkdownEditorView) {
            self.parent = parent
        }

        func textDidChange(_ notification: Notification) {
            guard let textView = notification.object as? NSTextView, !isFormatting else { return }
            self.parent.text = textView.string
            highlight(textView: textView)
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
                .foregroundColor: NSColor.textColor
            ], range: fullRange)
            
            let string = textStorage.string
            
            // Highlight Headers: # Header
            if let headerRegex = try? NSRegularExpression(pattern: "^#{1,6}\\s+.*$", options: .anchorsMatchLines) {
                let matches = headerRegex.matches(in: string, range: fullRange)
                for match in matches {
                    textStorage.addAttribute(.font, value: NSFont.monospacedSystemFont(ofSize: 18, weight: .bold), range: match.range)
                    textStorage.addAttribute(.foregroundColor, value: NSColor.controlAccentColor, range: match.range)
                }
            }
            
            // Highlight Wikilinks: [[Link]]
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
}
