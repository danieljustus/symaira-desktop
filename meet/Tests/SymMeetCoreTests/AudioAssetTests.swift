import CryptoKit
import Foundation
import XCTest

@testable import SymMeetCore

final class AudioAssetProbeTests: XCTestCase {
  func testProbesWAVWithStableMetadataAndHash() async throws {
    let root = try makeTemporaryDirectory()
    defer { try? FileManager.default.removeItem(at: root) }
    let source = root.appending(path: "sample.wav")
    try writeWAV(to: source, sampleRate: 16_000, durationMS: 200)

    let asset = try await AudioAssetProbe().probe(source, allowedRoot: root)

    XCTAssertEqual(asset.metadata.container, "wav")
    XCTAssertEqual(asset.metadata.durationMS, 200)
    XCTAssertEqual(asset.metadata.channelCount, 1)
    XCTAssertEqual(asset.metadata.sampleRate, 16_000)
    XCTAssertEqual(asset.metadata.sha256.count, 64)
    XCTAssertEqual(asset.metadata.sha256, try sha256(source))
  }

  func testRejectsSymlinkThatLeavesApprovedRoot() async throws {
    let root = try makeTemporaryDirectory()
    let outside = try makeTemporaryDirectory()
    defer {
      try? FileManager.default.removeItem(at: root)
      try? FileManager.default.removeItem(at: outside)
    }
    let source = outside.appending(path: "outside.wav")
    try writeWAV(to: source, sampleRate: 16_000, durationMS: 100)
    let link = root.appending(path: "linked.wav")
    try FileManager.default.createSymbolicLink(at: link, withDestinationURL: source)

    do {
      _ = try await AudioAssetProbe().probe(link, allowedRoot: root)
      XCTFail("Expected path validation to reject the symlink escape")
    } catch let error as AudioError {
      XCTAssertEqual(error, .outsideApprovedPath)
    }
  }
}

final class AudioAssetImporterTests: XCTestCase {
  func testCopiesOriginalAtomicallyAndReadsBoundedNormalizedChunks() async throws {
    let root = try makeTemporaryDirectory()
    let meeting = root.appending(path: "meeting")
    try FileManager.default.createDirectory(at: meeting, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: root) }
    let source = root.appending(path: "source.wav")
    try writeWAV(to: source, sampleRate: 8_000, durationMS: 2_500)

    let asset = try await AudioAssetProbe().probe(source, allowedRoot: root)
    let imported = try await AudioAssetImporter().importAsset(asset, into: meeting)

    XCTAssertEqual(imported.relativePath, "audio/original.wav")
    XCTAssertTrue(
      FileManager.default.fileExists(atPath: meeting.appending(path: imported.relativePath).path))

    var chunks: [AudioSampleChunk] = []
    for try await chunk in AudioSampleReader(url: meeting.appending(path: imported.relativePath))
      .chunks()
    {
      chunks.append(chunk)
    }
    XCTAssertGreaterThan(chunks.count, 1)
    XCTAssertEqual(chunks.first?.startMS, 0)
    XCTAssertEqual(chunks.last?.endMS, 2_500)
    XCTAssertTrue(chunks.allSatisfy { $0.samples.count <= 16_000 })
  }

  func testCancelledImportLeavesNoTemporaryCopy() async throws {
    let root = try makeTemporaryDirectory()
    let meeting = root.appending(path: "meeting")
    try FileManager.default.createDirectory(at: meeting, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: root) }
    let source = root.appending(path: "source.wav")
    try writeWAV(to: source, sampleRate: 16_000, durationMS: 100)
    let asset = try await AudioAssetProbe().probe(source, allowedRoot: root)

    let task = Task {
      try await AudioAssetImporter().importAsset(asset, into: meeting)
    }
    task.cancel()

    do {
      _ = try await task.value
      XCTFail("Expected cancellation")
    } catch let error as AudioError {
      XCTAssertEqual(error, .cancelled)
    }
    let audioDirectory = meeting.appending(path: "audio")
    XCTAssertFalse(
      FileManager.default.fileExists(atPath: audioDirectory.appending(path: "original.wav").path))
  }
}

private func makeTemporaryDirectory() throws -> URL {
  let directory = FileManager.default.temporaryDirectory.appending(
    path: "symmeet-audio-\(UUID().uuidString)")
  try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
  return directory
}

private func writeWAV(to url: URL, sampleRate: Int, durationMS: Int) throws {
  let frameCount = sampleRate * durationMS / 1_000
  var data = Data()
  data.append(contentsOf: Array("RIFF".utf8))
  appendUInt32(UInt32(36 + frameCount * 2), to: &data)
  data.append(contentsOf: Array("WAVEfmt ".utf8))
  appendUInt32(16, to: &data)
  appendUInt16(1, to: &data)
  appendUInt16(1, to: &data)
  appendUInt32(UInt32(sampleRate), to: &data)
  appendUInt32(UInt32(sampleRate * 2), to: &data)
  appendUInt16(2, to: &data)
  appendUInt16(16, to: &data)
  data.append(contentsOf: Array("data".utf8))
  appendUInt32(UInt32(frameCount * 2), to: &data)
  for frame in 0..<frameCount {
    let sample = Int16((sin(Double(frame) / 40) * 1_000).rounded())
    appendUInt16(UInt16(bitPattern: sample), to: &data)
  }
  try data.write(to: url)
}

private func appendUInt16(_ value: UInt16, to data: inout Data) {
  data.append(UInt8(value & 0xff))
  data.append(UInt8((value >> 8) & 0xff))
}

private func appendUInt32(_ value: UInt32, to data: inout Data) {
  data.append(UInt8(value & 0xff))
  data.append(UInt8((value >> 8) & 0xff))
  data.append(UInt8((value >> 16) & 0xff))
  data.append(UInt8((value >> 24) & 0xff))
}

private func sha256(_ url: URL) throws -> String {
  let digest = SHA256.hash(data: try Data(contentsOf: url))
  return digest.map { String(format: "%02x", $0) }.joined()
}
