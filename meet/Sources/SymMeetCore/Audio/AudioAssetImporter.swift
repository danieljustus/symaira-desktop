import Foundation

public struct AudioAssetImporter: Sendable {
  public init() {}

  public func importAsset(
    _ asset: AudioAsset,
    into meetingDirectory: URL,
    fileName: String = "original"
  ) async throws -> AudioAssetImportResult {
    if Task.isCancelled { throw AudioError.cancelled }
    let audioDirectory = meetingDirectory.appending(path: "audio", directoryHint: .isDirectory)
    let fileExtension = asset.fileExtension.isEmpty ? "audio" : asset.fileExtension
    let destination = audioDirectory.appending(
      path: "\(fileName).\(fileExtension)", directoryHint: .notDirectory)
    let temporary = audioDirectory.appending(
      path: ".\(destination.lastPathComponent).\(UUID().uuidString).tmp",
      directoryHint: .notDirectory)

    do {
      try FileManager.default.createDirectory(at: audioDirectory, withIntermediateDirectories: true)
      guard !FileManager.default.fileExists(atPath: destination.path) else {
        throw AudioError.destinationExists
      }

      let input = try FileHandle(forReadingFrom: asset.sourceURL)
      let output = try FileHandle(forWritingTo: createEmptyFile(at: temporary))
      defer {
        try? input.close()
        try? output.close()
        try? FileManager.default.removeItem(at: temporary)
      }

      while let data = try input.read(upToCount: 1024 * 1024), !data.isEmpty {
        if Task.isCancelled { throw AudioError.cancelled }
        try output.write(contentsOf: data)
      }
      try output.synchronize()
      try output.close()
      try input.close()
      try FileManager.default.moveItem(at: temporary, to: destination)

      return AudioAssetImportResult(
        relativePath: "audio/\(destination.lastPathComponent)", metadata: asset.metadata)
    } catch let error as AudioError {
      throw error
    } catch is CancellationError {
      throw AudioError.cancelled
    } catch {
      throw AudioError.operationFailed
    }
  }

  private func createEmptyFile(at url: URL) throws -> URL {
    guard FileManager.default.createFile(atPath: url.path, contents: nil) else {
      throw AudioError.operationFailed
    }
    return url
  }
}
