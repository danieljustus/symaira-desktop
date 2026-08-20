import Darwin
import Foundation

enum AtomicFileWriter {
  static func write(_ data: Data, to destination: URL) throws {
    let fileManager = FileManager.default
    let directory = destination.deletingLastPathComponent()
    let temporary = directory.appending(
      path: ".\(destination.lastPathComponent).\(UUID().uuidString).tmp")

    guard fileManager.createFile(atPath: temporary.path, contents: nil) else {
      throw StoreError.operationFailed
    }

    chmod(temporary.path, 0o600)

    do {
      let handle = try FileHandle(forWritingTo: temporary)
      try handle.write(contentsOf: data)
      try handle.synchronize()
      try handle.close()

      guard rename(temporary.path, destination.path) == 0 else {
        throw StoreError.operationFailed
      }

      try synchronize(directory: directory)
    } catch {
      try? fileManager.removeItem(at: temporary)
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  static func append(_ data: Data, to destination: URL) throws {
    do {
      let handle = try FileHandle(forWritingTo: destination)
      do {
        try handle.seekToEnd()
        try handle.write(contentsOf: data)
        try handle.synchronize()
        try handle.close()
      } catch {
        try? handle.close()
        throw error
      }
    } catch {
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  private static func synchronize(directory: URL) throws {
    let descriptor = open(directory.path, O_RDONLY)
    guard descriptor >= 0 else { throw StoreError.operationFailed }
    defer { _ = close(descriptor) }
    guard fsync(descriptor) == 0 else { throw StoreError.operationFailed }
  }
}
