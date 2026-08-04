import XCTest
import AppKit
@testable import SymDesk

/// Regression tests for issue #308: image paste/drop must surface a visible
/// error instead of silently falling through to a plain-text paste when the
/// image cannot be stored as a vault asset.
final class MarkdownTextViewTests: XCTestCase {

    private func makeTextView() -> MarkdownTextView {
        MarkdownTextView(frame: NSRect(x: 0, y: 0, width: 400, height: 300))
    }

    /// A small valid PNG payload (1x1 transparent pixel).
    private func makePNGData() -> Data {
        let rep = NSBitmapImageRep(
            bitmapDataPlanes: nil,
            pixelsWide: 1,
            pixelsHigh: 1,
            bitsPerSample: 8,
            samplesPerPixel: 4,
            hasAlpha: true,
            isPlanar: false,
            colorSpaceName: .deviceRGB,
            bytesPerRow: 0,
            bitsPerPixel: 0
        )!
        return rep.representation(using: .png, properties: [:])!
    }

    private func pasteboard(with png: Data) -> NSPasteboard {
        let pb = NSPasteboard(name: NSPasteboard.Name("symdesk-test-image-paste"))
        pb.clearContents()
        pb.setData(png, forType: .png)
        return pb
    }

    func testFailedImageStoreReportsErrorAndConsumesPaste() {
        let textView = makeTextView()
        var reported: [String] = []
        textView.onImageData = { _, _ in nil } // storage failure
        textView.onImageError = { reported.append($0) }

        let consumed = textView.insertImages(from: pasteboard(with: makePNGData()))

        XCTAssertTrue(consumed, "a failed image paste must be consumed, not fall through to text paste")
        XCTAssertEqual(reported.count, 1)
        XCTAssertFalse(reported[0].isEmpty)
        XCTAssertEqual(textView.string, "", "no image markdown should be inserted on failure")
    }

    func testSuccessfulImagePasteInsertsMarkdownLink() {
        let textView = makeTextView()
        textView.onImageData = { _, ext in
            XCTAssertEqual(ext, "png")
            return "![pasted](assets/pasted-1.png)"
        }

        let consumed = textView.insertImages(from: pasteboard(with: makePNGData()))

        XCTAssertTrue(consumed)
        XCTAssertEqual(textView.string, "![pasted](assets/pasted-1.png)")
    }

    func testNonImagePasteboardFallsThroughToDefaultBehavior() {
        let textView = makeTextView()
        textView.onImageData = { _, _ in "![x](y)" }

        let pb = NSPasteboard(name: NSPasteboard.Name("symdesk-test-text-paste"))
        pb.clearContents()
        pb.setString("plain text", forType: .string)

        let consumed = textView.insertImages(from: pb)

        XCTAssertFalse(consumed, "non-image content must fall through to the default paste handler")
        XCTAssertEqual(textView.string, "")
    }

    func testMissingHandlerFallsThrough() {
        let textView = makeTextView()
        textView.onImageData = nil

        let consumed = textView.insertImages(from: pasteboard(with: makePNGData()))

        XCTAssertFalse(consumed)
    }

    func testImageFileURLThatCannotBeReadReportsErrorAndConsumes() {
        let textView = makeTextView()
        var reported: [String] = []
        textView.onImageData = { _, _ in "![x](y)" }
        textView.onImageError = { reported.append($0) }

        let pb = NSPasteboard(name: NSPasteboard.Name("symdesk-test-file-paste"))
        pb.clearContents()
        // A non-existent file URL with an image extension.
        pb.writeObjects([URL(fileURLWithPath: "/nonexistent/path/screenshot.png") as NSURL])

        let consumed = textView.insertImages(from: pb)

        XCTAssertTrue(consumed, "an unreadable image file must be consumed, not silently dropped")
        XCTAssertEqual(reported.count, 1)
        XCTAssertEqual(textView.string, "")
    }
}
