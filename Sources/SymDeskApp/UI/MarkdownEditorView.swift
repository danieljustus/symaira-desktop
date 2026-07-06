import SwiftUI
import AppKit

struct MarkdownEditorView: NSViewRepresentable {
    @Binding var text: String
    var onLinkClick: ((String) -> Void)?

    func makeCoordinator() -> Coordinator {
        Coordinator(self)
    }

    func makeNSView(context: Context) -> NSScrollView {
        let scrollView = NSTextView.scrollableTextView()
        let textView = scrollView.documentView as! NSTextView
        
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

    class Coordinator: NSObject, NSTextViewDelegate {
        var parent: MarkdownEditorView
        private var isFormatting = false

        init(_ parent: MarkdownEditorView) {
            self.parent = parent
        }

        func textDidChange(_ notification: Notification) {
            guard let textView = notification.object as? NSTextView, !isFormatting else { return }
            self.parent.text = textView.string
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
