import SwiftUI
import PDFKit

struct PDFKitRepresentedView: NSViewRepresentable {
    let url: URL?
    
    func makeNSView(context: Context) -> PDFView {
        let pdfView = PDFView()
        pdfView.autoScales = true
        pdfView.displayMode = .singlePageContinuous
        pdfView.displaysPageBreaks = true
        pdfView.backgroundColor = NSColor(red: 18/255, green: 17/255, blue: 14/255, alpha: 0.65)
        return pdfView
    }
    
    func updateNSView(_ pdfView: PDFView, context: Context) {
        if let url = url, FileManager.default.fileExists(atPath: url.path) {
            // Re-load the document if the URL has changed or been updated
            if let document = PDFDocument(url: url) {
                // Keep scroll position if document is the same, or reload
                pdfView.document = document
                pdfView.autoScales = true
            } else {
                pdfView.document = nil
            }
        } else {
            pdfView.document = nil
        }
    }
}
