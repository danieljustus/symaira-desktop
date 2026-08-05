import Foundation
import VisionKit

/// Wraps `VNDocumentCameraViewController` so the SwiftUI workspace can
/// present the system document scanner with multi-page support, edge
/// detection and rescan. The scanner is a device-only surface — the
/// simulator shows an "unsupported" placeholder instead (see
/// `MobileScanView`).
final class MobileDocumentScanner: NSObject, VNDocumentCameraViewControllerDelegate, @unchecked Sendable {
    var onComplete: (@Sendable ([UIImage]) -> Void)?
    var onCancel: (@Sendable () -> Void)?

    func makeViewController() -> VNDocumentCameraViewController {
        let controller = VNDocumentCameraViewController()
        controller.delegate = self
        return controller
    }

    func documentCameraViewController(
        _ controller: VNDocumentCameraViewController,
        didFinishWith scan: VNDocumentCameraScan
    ) {
        var pages: [UIImage] = []
        for index in 0..<scan.pageCount {
            pages.append(scan.imageOfPage(at: index))
        }
        onComplete?(pages)
    }

    func documentCameraViewControllerDidCancel(_ controller: VNDocumentCameraViewController) {
        onCancel?()
    }

    func documentCameraViewController(
        _ controller: VNDocumentCameraViewController,
        didFailWithError error: Error
    ) {
        onCancel?()
    }
}

/// Renders captured pages into one PDF (the ingest pipeline's expected
/// shape) or a JPEG option. Page images are drawn at their native
/// resolution; the PDF uses the first page's orientation for all pages.
enum MobileScanPDFBuilder {
    /// Builds a single PDF from pages in the given order.
    static func pdf(from pages: [UIImage]) -> Data? {
        guard let first = pages.first else { return nil }

        let pageSize = first.size
        let renderer = UIGraphicsPDFRenderer(
            bounds: CGRect(origin: .zero, size: pageSize),
            format: UIGraphicsPDFRendererFormat()
        )

        return renderer.pdfData { context in
            for page in pages {
                context.beginPage()
                page.draw(in: CGRect(origin: .zero, size: pageSize))
            }
        }
    }

    /// JPEG of the first page — the photo option for single-page scans.
    static func jpeg(from page: UIImage, quality: CGFloat = 0.85) -> Data? {
        page.jpegData(compressionQuality: quality)
    }
}
