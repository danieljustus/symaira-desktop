import Foundation
import SymMeetCore
@preconcurrency import WhisperKit

/// Downloads a selected WhisperKit model into ModelStore's staging area and
/// publishes it atomically. Downloads only happen when this method is called.
public actor WhisperKitModelInstaller {
  /// Test seam: the download step, injected so unit tests never hit the
  /// network or Hugging Face. Production uses `defaultDownloader`.
  internal typealias Downloader = @Sendable (
    _ variant: String,
    _ downloadBase: URL,
    _ progress: @escaping @Sendable (Double) -> Void
  ) async throws -> URL

  private let store: ModelStore
  private let downloader: Downloader

  public init(store: ModelStore = ModelStore()) {
    self.store = store
    self.downloader = Self.defaultDownloader
  }

  /// Test seam: injects a fake downloader.
  internal init(store: ModelStore, downloader: @escaping Downloader) {
    self.store = store
    self.downloader = downloader
  }

  public func install(
    id: String,
    progress: (@Sendable (Double) -> Void)? = nil
  ) async throws -> ModelRecord {
    let descriptor = try await descriptor(for: id)
    let staging = try await store.prepareDownload(for: id)
    let downloadBase = staging.appending(path: "download", directoryHint: .isDirectory)
    try FileManager.default.createDirectory(at: downloadBase, withIntermediateDirectories: true)

    do {
      let downloaded = try await downloader(
        descriptor.upstreamRevision,
        downloadBase
      ) { value in progress?(value) }
      let payload = staging.appending(path: "payload", directoryHint: .isDirectory)
      try FileManager.default.copyItem(at: downloaded, to: payload)
      return try await store.publish(id, from: staging)
    } catch is CancellationError {
      try? FileManager.default.removeItem(at: staging)
      throw CancellationError()
    } catch let error as ModelError {
      try? FileManager.default.removeItem(at: staging)
      throw error
    } catch {
      try? FileManager.default.removeItem(at: staging)
      throw WhisperKitEngineError.modelUnavailable
    }
  }

  private static func defaultDownloader(
    _ variant: String,
    _ downloadBase: URL,
    _ progress: @escaping @Sendable (Double) -> Void
  ) async throws -> URL {
    try await WhisperKit.download(
      variant: variant,
      downloadBase: downloadBase,
      progressCallback: { value in progress(value.fractionCompleted) }
    )
  }

  private func descriptor(for id: String) async throws -> ModelDescriptor {
    let catalog = await store.catalog
    guard let descriptor = catalog.descriptor(for: id) else {
      throw ModelError.unknownModel
    }
    guard descriptor.engineID == "whisperkit" else {
      throw WhisperKitEngineError.unsupportedModel
    }
    return descriptor
  }
}
