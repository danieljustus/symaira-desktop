import CryptoKit
import Foundation

public actor ModelStore {
  public let root: URL
  public let catalog: ModelCatalog

  private var activeModelIDs: Set<String> = []

  public init(root: URL = SymMeetPaths().modelsDirectory, catalog: ModelCatalog = .beta) {
    self.root = root.standardizedFileURL
    self.catalog = catalog
  }

  public func list() throws -> [ModelRecord] {
    try prepareRoot()
    return try catalog.descriptors.map { descriptor in
      try record(for: descriptor)
    }
  }

  public func prepareDownload(for id: String) throws -> URL {
    let descriptor = try descriptor(for: id)
    try prepareRoot()
    let directory = root.appending(path: ".\(descriptor.id).downloading-\(UUID().uuidString)")
    do {
      try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false)
      return directory
    } catch {
      throw ModelError.operationFailed
    }
  }

  public func publish(
    _ id: String,
    from temporaryDirectory: URL,
    sha256: String? = nil,
    installedAt: Date = Date()
  ) throws -> ModelRecord {
    let descriptor = try descriptor(for: id)
    try requireDirectory(temporaryDirectory)
    try prepareRoot()

    let destination = modelDirectory(for: descriptor.id)
    if FileManager.default.fileExists(atPath: destination.path) {
      try? FileManager.default.removeItem(at: temporaryDirectory)
      return try verify(id: id)
    }

    let staging = root.appending(path: ".\(descriptor.id).publishing-\(UUID().uuidString)")
    do {
      try FileManager.default.copyItem(at: temporaryDirectory, to: staging)

      let payload = staging.appending(path: "payload", directoryHint: .isDirectory)
      let computedSHA = try sha256OfDirectory(at: payload)

      if let expected = descriptor.expectedSHA256, expected != computedSHA {
        throw ModelError.corruptModel
      }

      try writeMetadata(
        ModelRecord(
          descriptor: descriptor,
          status: .installed,
          installedAt: installedAt,
          sha256: computedSHA),
        to: staging)
      try FileManager.default.moveItem(at: staging, to: destination)
      try? FileManager.default.removeItem(at: temporaryDirectory)
      return try verify(id: id)
    } catch let error as ModelError {
      try? FileManager.default.removeItem(at: staging)
      throw error
    } catch {
      try? FileManager.default.removeItem(at: staging)
      throw ModelError.operationFailed
    }
  }

  public func verify(id: String) throws -> ModelRecord {
    let descriptor = try descriptor(for: id)
    let directory = modelDirectory(for: descriptor.id)
    guard FileManager.default.fileExists(atPath: directory.path) else {
      throw ModelError.modelNotInstalled
    }
    guard let metadata = try? readMetadata(from: directory) else {
      throw ModelError.corruptModel
    }
    guard metadata.descriptor == descriptor else { throw ModelError.corruptModel }
    guard descriptor.supportedArchitectures.contains(Self.currentArchitecture) else {
      throw ModelError.incompatibleModel
    }
    let payload = directory.appending(path: "payload", directoryHint: .isDirectory)
    guard FileManager.default.fileExists(atPath: payload.path) else {
      throw ModelError.corruptModel
    }
    if let storedSHA = metadata.sha256 {
      let computedSHA = try sha256OfDirectory(at: payload)
      guard computedSHA == storedSHA else {
        throw ModelError.corruptModel
      }
    }
    return ModelRecord(
      descriptor: descriptor,
      status: .installed,
      installedAt: metadata.installedAt,
      sha256: metadata.sha256)
  }

  public func markInUse(_ id: String) throws {
    _ = try descriptor(for: id)
    activeModelIDs.insert(id)
  }

  public func markAvailable(_ id: String) throws {
    _ = try descriptor(for: id)
    activeModelIDs.remove(id)
  }

  @discardableResult
  public func remove(id: String) throws -> Bool {
    let descriptor = try descriptor(for: id)
    guard !activeModelIDs.contains(descriptor.id) else { throw ModelError.inUse }
    let directory = modelDirectory(for: descriptor.id)
    guard FileManager.default.fileExists(atPath: directory.path) else { return false }
    do {
      try FileManager.default.removeItem(at: directory)
      return true
    } catch {
      throw ModelError.operationFailed
    }
  }

  private func record(for descriptor: ModelDescriptor) throws -> ModelRecord {
    let directory = modelDirectory(for: descriptor.id)
    if FileManager.default.fileExists(atPath: directory.path) {
      guard let metadata = try? readMetadata(from: directory) else {
        return ModelRecord(descriptor: descriptor, status: .corrupt)
      }
      guard descriptor.supportedArchitectures.contains(Self.currentArchitecture) else {
        return ModelRecord(
          descriptor: descriptor,
          status: .incompatible,
          installedAt: metadata.installedAt,
          sha256: metadata.sha256)
      }
      let payload = directory.appending(path: "payload", directoryHint: .isDirectory)
      guard FileManager.default.fileExists(atPath: payload.path) else {
        return ModelRecord(descriptor: descriptor, status: .corrupt)
      }
      return ModelRecord(
        descriptor: descriptor,
        status: .installed,
        installedAt: metadata.installedAt,
        sha256: metadata.sha256)
    }

    let downloading = try FileManager.default.contentsOfDirectory(
      at: root, includingPropertiesForKeys: nil, options: []
    ).contains { $0.lastPathComponent.hasPrefix(".\(descriptor.id).downloading-") }
    return ModelRecord(descriptor: descriptor, status: downloading ? .downloading : .available)
  }

  private func descriptor(for id: String) throws -> ModelDescriptor {
    guard !id.isEmpty, !id.contains("/"), !id.contains("\\"), !id.contains("..")
    else { throw ModelError.invalidIdentifier }
    guard let descriptor = catalog.descriptor(for: id) else { throw ModelError.unknownModel }
    return descriptor
  }

  private func modelDirectory(for id: String) -> URL {
    root.appending(path: id, directoryHint: .isDirectory)
  }

  private func prepareRoot() throws {
    do {
      try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
      try FileManager.default.setAttributes(
        [.posixPermissions: 0o700], ofItemAtPath: root.path)
    } catch {
      throw ModelError.operationFailed
    }
  }

  private func requireDirectory(_ url: URL) throws {
    var isDirectory: ObjCBool = false
    guard FileManager.default.fileExists(atPath: url.path, isDirectory: &isDirectory),
      isDirectory.boolValue
    else { throw ModelError.invalidSource }
  }

  private func writeMetadata(_ record: ModelRecord, to directory: URL) throws {
    do {
      let data = try ContractCodec.encoder(prettyPrinted: true).encode(record)
      try data.write(to: directory.appending(path: "model.json"), options: .atomic)
    } catch {
      throw ModelError.operationFailed
    }
  }

  private func readMetadata(from directory: URL) throws -> ModelRecord {
    try ContractCodec.decoder().decode(
      ModelRecord.self,
      from: Data(contentsOf: directory.appending(path: "model.json")))
  }

  private func sha256OfDirectory(at url: URL) throws -> String {
    let fileManager = FileManager.default
    guard let enumerator = fileManager.enumerator(
      at: url,
      includingPropertiesForKeys: [.isRegularFileKey])
    else {
      throw ModelError.operationFailed
    }

    var entries: [(String, URL)] = []
    while let fileURL = enumerator.nextObject() as? URL {
      guard
        (try? fileURL.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true
      else { continue }
      let relativePath = String(fileURL.path.dropFirst(url.path.count + 1))
      entries.append((relativePath, fileURL))
    }
    entries.sort { $0.0 < $1.0 }

    var hasher = SHA256()
    for (relativePath, fileURL) in entries {
      hasher.update(data: Data(relativePath.utf8))
      let handle = try FileHandle(forReadingFrom: fileURL)
      defer { try? handle.close() }
      while let chunk = try handle.read(upToCount: 1024 * 1024), !chunk.isEmpty {
        hasher.update(data: chunk)
      }
    }
    return hasher.finalize().map { String(format: "%02x", $0) }.joined()
  }

  private static var currentArchitecture: String {
    #if arch(arm64)
      return "arm64"
    #elseif arch(x86_64)
      return "x86_64"
    #else
      return "unknown"
    #endif
  }
}
