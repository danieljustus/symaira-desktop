import Foundation
import XCTest

@testable import SymMeetCore

final class ModelStoreTests: XCTestCase {
  func testModelsPublishAtomicallyAndCannotBeRemovedWhileInUse() async throws {
    let root = try makeTemporaryDirectory()
    defer { try? FileManager.default.removeItem(at: root) }
    let source = root.appending(path: "download")
    try FileManager.default.createDirectory(
      at: source.appending(path: "payload"), withIntermediateDirectories: true)
    try Data("model".utf8).write(to: source.appending(path: "payload/weights.bin"))

    let store = ModelStore(root: root.appending(path: "models"))
    let available = try await store.list()
    XCTAssertEqual(available.first { $0.descriptor.id == "tiny" }?.status, .available)

    let published = try await store.publish("tiny", from: source, sha256: "abc")
    XCTAssertEqual(published.status, .installed)
    let verified = try await store.verify(id: "tiny")
    // SHA256 is now always computed from the actual payload, not caller-provided
    XCTAssertFalse(verified.sha256?.isEmpty ?? true, "sha256 must be computed and stored")
    XCTAssertTrue(
      FileManager.default.fileExists(atPath: root.appending(path: "models/tiny/model.json").path))

    try await store.markInUse("tiny")
    do {
      _ = try await store.remove(id: "tiny")
      XCTFail("Expected in-use model removal to fail")
    } catch let error as ModelError {
      XCTAssertEqual(error, .inUse)
    }
    try await store.markAvailable("tiny")
    let removed = try await store.remove(id: "tiny")
    XCTAssertTrue(removed)
  }

  func testDownloadingAndCorruptStatesAreVisible() async throws {
    let root = try makeTemporaryDirectory()
    defer { try? FileManager.default.removeItem(at: root) }
    let store = ModelStore(root: root.appending(path: "models"))
    _ = try await store.prepareDownload(for: "tiny")
    let downloading = try await store.list().first { $0.descriptor.id == "tiny" }?.status
    XCTAssertEqual(downloading, .downloading)

    let corrupt = root.appending(path: "models/large-v3-v20240930_626MB")
    try FileManager.default.createDirectory(at: corrupt, withIntermediateDirectories: true)
    let record = try await store.list().first { $0.descriptor.id == "large-v3-v20240930_626MB" }
    XCTAssertEqual(record?.status, .corrupt)
  }

  func testConcurrentInstallAttemptsProduceOneArtifact() async throws {
    let root = try makeTemporaryDirectory()
    defer { try? FileManager.default.removeItem(at: root) }

    func makeSource(_ name: String) throws -> URL {
      let source = root.appending(path: name)
      try FileManager.default.createDirectory(
        at: source.appending(path: "payload"), withIntermediateDirectories: true)
      try Data("model".utf8).write(to: source.appending(path: "payload/weights.bin"))
      return source
    }
    let sourceA = try makeSource("download-a")
    let sourceB = try makeSource("download-b")

    let store = ModelStore(root: root.appending(path: "models"))
    async let first = store.publish("tiny", from: sourceA, sha256: "a")
    async let second = store.publish("tiny", from: sourceB, sha256: "b")
    let results = try await [first, second]

    XCTAssertTrue(results.allSatisfy { $0.status == .installed })
    XCTAssertEqual(Set(results.map(\.sha256)).count, 1, "both calls must observe the same winner")

    let entries = try FileManager.default.contentsOfDirectory(
      at: root.appending(path: "models"), includingPropertiesForKeys: nil
    ).map(\.lastPathComponent).filter { $0 == "tiny" || $0.hasPrefix(".tiny.") }
    XCTAssertEqual(entries, ["tiny"], "no leftover staging or duplicate artifact")

    let verified = try await store.verify(id: "tiny")
    XCTAssertEqual(verified.sha256, results.first?.sha256)
  }
}

private func makeTemporaryDirectory() throws -> URL {
  let directory = FileManager.default.temporaryDirectory.appending(
    path: "symmeet-model-\(UUID().uuidString)")
  try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
  return directory
}
