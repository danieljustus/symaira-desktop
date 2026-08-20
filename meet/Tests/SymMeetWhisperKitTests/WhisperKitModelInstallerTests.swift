import Foundation
import SymMeetCore
import XCTest

@testable import SymMeetWhisperKit

/// A fake downloader that materializes a payload file under the given
/// download base and returns it, exactly like `WhisperKit.download` would.
private func makeFakeDownloader(
  payloadFile: String = "model.bin",
  error: Error? = nil
) -> WhisperKitModelInstaller.Downloader {
  { variant, downloadBase, progress in
    if let error { throw error }
    try Data("fake-model-\(variant)".utf8).write(
      to: downloadBase.appending(path: payloadFile))
    progress(1.0)
    return downloadBase
  }
}

final class WhisperKitModelInstallerTests: XCTestCase {
  private var root: URL!

  override func setUpWithError() throws {
    root = FileManager.default.temporaryDirectory.appending(
      path: "model-installer-tests-\(UUID().uuidString)", directoryHint: .isDirectory)
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
  }

  override func tearDownWithError() throws {
    try? FileManager.default.removeItem(at: root)
  }

  func testInstallPublishesModelRecord() async throws {
    let store = ModelStore(root: root, catalog: .beta)
    let installer = WhisperKitModelInstaller(
      store: store, downloader: makeFakeDownloader())

    let record = try await installer.install(id: "tiny")

    XCTAssertEqual(record.status, .installed)
    XCTAssertEqual(record.descriptor.id, "tiny")
    // The model directory is published under the store root.
    let payload = root.appending(path: "tiny")
      .appending(path: "payload", directoryHint: .isDirectory)
    XCTAssertTrue(FileManager.default.fileExists(atPath: payload.path))
    XCTAssertTrue(
      FileManager.default.fileExists(
        atPath: payload.appending(path: "model.bin").path))

    // A subsequent verify succeeds and reports the same record.
    let verified = try await store.verify(id: "tiny")
    XCTAssertEqual(verified.status, .installed)
    XCTAssertEqual(verified.sha256, record.sha256)
  }

  func testInstallUnknownModelThrows() async {
    let store = ModelStore(root: root, catalog: .beta)
    let installer = WhisperKitModelInstaller(
      store: store, downloader: makeFakeDownloader())

    do {
      _ = try await installer.install(id: "does-not-exist")
      XCTFail("Expected unknownModel error")
    } catch let error as ModelError {
      XCTAssertEqual(error, .unknownModel)
    } catch {
      XCTFail("Unexpected error: \(error)")
    }
  }

  func testInstallNonWhisperKitModelThrowsUnsupported() async throws {
    // A catalog whose only model belongs to another engine.
    let foreignCatalog = ModelCatalog(descriptors: [
      ModelDescriptor(
        id: "diar-model",
        engineID: "speakerkit",
        displayName: "Diar",
        source: "https://example.com",
        license: "MIT",
        expectedSizeBytes: 1_000,
        upstreamRevision: "rev-1",
        supportedArchitectures: ["arm64"])
    ])
    let store = ModelStore(root: root, catalog: foreignCatalog)
    let installer = WhisperKitModelInstaller(
      store: store, downloader: makeFakeDownloader())

    do {
      _ = try await installer.install(id: "diar-model")
      XCTFail("Expected unsupportedModel error")
    } catch let error as WhisperKitEngineError {
      XCTAssertEqual(error, .unsupportedModel)
    } catch {
      XCTFail("Unexpected error: \(error)")
    }
  }

  func testDownloadFailureCleansStagingAndThrowsModelUnavailable() async throws {
    struct DownloadError: Error {}
    let store = ModelStore(root: root, catalog: .beta)
    let installer = WhisperKitModelInstaller(
      store: store, downloader: makeFakeDownloader(error: DownloadError()))

    do {
      _ = try await installer.install(id: "tiny")
      XCTFail("Expected modelUnavailable error")
    } catch let error as WhisperKitEngineError {
      XCTAssertEqual(error, .modelUnavailable)
    } catch {
      XCTFail("Unexpected error: \(error)")
    }

    // No leftover staging directories survive a failed install.
    let leftovers = try FileManager.default.contentsOfDirectory(atPath: root.path)
      .filter { $0.hasPrefix(".tiny.downloading-") }
    XCTAssertTrue(leftovers.isEmpty)
  }

  func testCancellationCleansStagingAndRethrows() async throws {
    let store = ModelStore(root: root, catalog: .beta)
    let installer = WhisperKitModelInstaller(
      store: store, downloader: makeFakeDownloader(error: CancellationError()))

    do {
      _ = try await installer.install(id: "tiny")
      XCTFail("Expected CancellationError")
    } catch is CancellationError {
      // expected
    } catch {
      XCTFail("Unexpected error: \(error)")
    }

    let leftovers = try FileManager.default.contentsOfDirectory(atPath: root.path)
      .filter { $0.hasPrefix(".tiny.downloading-") }
    XCTAssertTrue(leftovers.isEmpty)
  }

  func testProgressCallbackReceivesDownloaderProgress() async throws {
    let collector = ProgressCollector()
    let store = ModelStore(root: root, catalog: .beta)
    let installer = WhisperKitModelInstaller(
      store: store,
      downloader: { variant, downloadBase, progress in
        progress(0.25)
        progress(1.0)
        try Data("fake-model-\(variant)".utf8).write(
          to: downloadBase.appending(path: "model.bin"))
        return downloadBase
      })

    _ = try await installer.install(id: "tiny") { value in
      collector.record(value)
    }

    XCTAssertEqual(collector.values, [0.25, 1.0])
  }
}

/// Serializes progress values collected from a @Sendable callback.
private final class ProgressCollector: @unchecked Sendable {
  private let lock = NSLock()
  private var storage: [Double] = []

  func record(_ value: Double) {
    lock.lock()
    storage.append(value)
    lock.unlock()
  }

  var values: [Double] {
    lock.lock()
    defer { lock.unlock() }
    return storage
  }
}
