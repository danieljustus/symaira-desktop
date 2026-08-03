import Foundation
import VisionKit
import XCTest
@testable import SymDeskMobile

/// Tests for the camera-scan pipeline (#326): PDF rendering from captured
/// pages and the queued delivery path into the consume folder (Files mode)
/// and via the write layer in server mode.
final class MobileScanTests: XCTestCase {

    private var tempDirectory: URL!

    override func setUpWithError() throws {
        tempDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("ScanTests-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: tempDirectory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: tempDirectory)
    }

    // MARK: - PDF builder

    func testPDFBuilderProducesValidSinglePagePDF() throws {
        let pages = try makePages(count: 1)
        let data = try XCTUnwrap(MobileScanPDFBuilder.pdf(from: pages))

        XCTAssertTrue(data.starts(with: Data("%PDF".utf8)), "must be a PDF")
        XCTAssertTrue(data.range(of: Data("%%EOF".utf8)) != nil, "PDF must be well terminated")
    }

    func testPDFBuilderMultiPageProducesLargerDocument() throws {
        let one = try XCTUnwrap(MobileScanPDFBuilder.pdf(from: try makePages(count: 1)))
        let three = try XCTUnwrap(MobileScanPDFBuilder.pdf(from: try makePages(count: 3)))
        // Three pages at the same resolution must exceed one page in size.
        XCTAssertGreaterThan(three.count, one.count)
    }

    func testPDFBuilderEmptyPagesReturnsNil() {
        XCTAssertNil(MobileScanPDFBuilder.pdf(from: []))
    }

    func testJPEGOptionProducesImageData() throws {
        let page = try makePages(count: 1)[0]
        let data = try XCTUnwrap(MobileScanPDFBuilder.jpeg(from: page))
        XCTAssertFalse(data.isEmpty)
        XCTAssertTrue(data.starts(with: Data([0xFF, 0xD8])), "JPEG magic bytes expected")
    }

    // MARK: - Delivery path (Files mode → consume folder)

    func testUploadQueuedIntoConsumeFolderViaWriteLayer() async throws {
        let vaultRoot = tempDirectory.appendingPathComponent("vault", isDirectory: true)
        try FileManager.default.createDirectory(at: vaultRoot, withIntermediateDirectories: true)

        let outbox = try MobileOutbox(directory: tempDirectory.appendingPathComponent("outbox", isDirectory: true))
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        await coordinator.setMode(MobileFilesWriteAdapter(vaultRoot: vaultRoot))

        // Exactly what MobileScanView.submit() does for a PDF scan.
        let pages = try makePages(count: 1)
        let pdf = try XCTUnwrap(MobileScanPDFBuilder.pdf(from: pages))
        let entry = MobileOutboxEntry(
            kind: .uploadOriginal,
            path: "scan-20260803-120000.pdf",
            originalData: pdf,
            originalFilename: "scan-20260803-120000.pdf",
            folder: nil // default consume folder
        )
        try await coordinator.enqueue(entry)

        // Files-mode uploads land in <vault>/inbox_watch (the watcher's
        // default consume folder) so the desktop pipeline picks them up.
        let target = vaultRoot.appendingPathComponent("inbox_watch/scan-20260803-120000.pdf")
        for _ in 0..<50 {
            if try FileManager.default.fileExists(atPath: target.path) {
                break
            }
            try? await Task.sleep(for: .milliseconds(20))
        }

        XCTAssertTrue(try FileManager.default.fileExists(atPath: target.path), "scan must be written into the consume folder")
        let written = try Data(contentsOf: target)
        XCTAssertEqual(written, pdf, "uploaded bytes must match the rendered PDF")
        let remaining = await coordinator.entries()
        XCTAssertTrue(remaining.isEmpty, "applied upload must leave the queue")
    }

    func testCustomConsumeFolderRespected() async throws {
        let vaultRoot = tempDirectory.appendingPathComponent("vault", isDirectory: true)
        try FileManager.default.createDirectory(at: vaultRoot, withIntermediateDirectories: true)

        let outbox = try MobileOutbox(directory: tempDirectory.appendingPathComponent("outbox", isDirectory: true))
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        await coordinator.setMode(MobileFilesWriteAdapter(vaultRoot: vaultRoot))

        let entry = MobileOutboxEntry(
            kind: .uploadOriginal,
            path: "scan.jpg",
            originalData: Data([0xFF, 0xD8, 0xFF]),
            originalFilename: "scan.jpg",
            folder: "meine-ablage"
        )
        try await coordinator.enqueue(entry)

        let target = vaultRoot.appendingPathComponent("meine-ablage/scan.jpg")
        for _ in 0..<50 {
            if try FileManager.default.fileExists(atPath: target.path) {
                break
            }
            try? await Task.sleep(for: .milliseconds(20))
        }
        XCTAssertTrue(try FileManager.default.fileExists(atPath: target.path))
    }

    func testOfflineScanStaysQueuedUntilModeSet() async throws {
        let outbox = try MobileOutbox(directory: tempDirectory.appendingPathComponent("outbox", isDirectory: true))
        let coordinator = MobileWriteCoordinator(outbox: outbox)
        // No mode yet — offline state.

        let entry = MobileOutboxEntry(
            kind: .uploadOriginal,
            path: "scan.pdf",
            originalData: Data("pdf".utf8),
            originalFilename: "scan.pdf",
            folder: nil
        )
        try await coordinator.enqueue(entry)
        try? await Task.sleep(for: .milliseconds(100))

        // Queued, not failed — will apply when a backend becomes active.
        let stored = await coordinator.entries().first
        XCTAssertEqual(stored?.state, .queued)
        let count = await coordinator.entries().count
        XCTAssertEqual(count, 1)
    }

    // MARK: - Fixtures

    private func makePages(count: Int) throws -> [UIImage] {
        // Build distinct images so multi-page PDFs differ in size.
        return try (0..<count).map { index in
            let size = CGSize(width: 200, height: 260)
            let renderer = UIGraphicsImageRenderer(size: size)
            let image = renderer.image { context in
                UIColor.systemBlue.setFill()
                context.fill(CGRect(origin: .zero, size: size))
                let text = "Page \(index + 1)" as NSString
                text.draw(
                    at: CGPoint(x: 20, y: 20),
                    withAttributes: [.foregroundColor: UIColor.white, .font: UIFont.systemFont(ofSize: 18)]
                )
            }
            return image
        }
    }
}
