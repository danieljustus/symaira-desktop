import AVFoundation
import CoreMedia
import Foundation
import Testing

@testable import SymMeetCapture

@Suite("TrackWriter")
struct TrackWriterTests {

  @Test("prepare creates file at expected URL")
  func prepareCreatesFile() async throws {
    let dir = FileManager.default.temporaryDirectory.appending(
      path: "TrackWriterTest-\(UUID().uuidString)", directoryHint: .isDirectory)
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: dir) }

    let url = dir.appending(path: "test.caf", directoryHint: .notDirectory)
    let writer = TrackWriter(url: url)
    let format = AVAudioFormat(standardFormatWithSampleRate: 48000, channels: 2)!
    try await writer.prepare(format: format)
    // After prepare the file should exist (AVAssetWriter creates it)
    #expect(FileManager.default.fileExists(atPath: url.path))
  }

  @Test("recordGapStart records a gap with nil end offset")
  func recordGapStart() async throws {
    let dir = FileManager.default.temporaryDirectory.appending(
      path: "TrackWriterTest-\(UUID().uuidString)", directoryHint: .isDirectory)
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: dir) }

    let url = dir.appending(path: "test.caf", directoryHint: .notDirectory)
    let writer = TrackWriter(url: url)
    let format = AVAudioFormat(standardFormatWithSampleRate: 48000, channels: 2)!
    try await writer.prepare(format: format)

    let offset = CMTime(seconds: 5, preferredTimescale: 48000)
    await writer.recordGapStart(reason: .pause, at: offset)
    let gaps = await writer.gaps
    #expect(gaps.count == 1)
    #expect(gaps[0].reason == .pause)
    #expect(gaps[0].endOffset == nil)
  }

  @Test("closeLastGap sets end offset")
  func closeLastGap() async throws {
    let dir = FileManager.default.temporaryDirectory.appending(
      path: "TrackWriterTest-\(UUID().uuidString)", directoryHint: .isDirectory)
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: dir) }

    let url = dir.appending(path: "test.caf", directoryHint: .notDirectory)
    let writer = TrackWriter(url: url)
    let format = AVAudioFormat(standardFormatWithSampleRate: 48000, channels: 2)!
    try await writer.prepare(format: format)

    let start = CMTime(seconds: 5, preferredTimescale: 48000)
    let end = CMTime(seconds: 10, preferredTimescale: 48000)
    await writer.recordGapStart(reason: .deviceLoss, at: start)
    await writer.closeLastGap(at: end)
    let gaps = await writer.gaps
    #expect(gaps[0].endOffset == end)
  }

  @Test("CaptureDiagnostics records duration correctly")
  func diagnosticsDuration() {
    var diag = CaptureDiagnostics()
    let base = Date(timeIntervalSinceReferenceDate: 0)
    diag.record(.init(kind: .started, timestamp: base, detail: nil))
    diag.record(.init(kind: .paused, timestamp: base.addingTimeInterval(10), detail: nil))
    diag.record(.init(kind: .resumed, timestamp: base.addingTimeInterval(15), detail: nil))
    diag.record(.init(kind: .stopped, timestamp: base.addingTimeInterval(30), detail: nil))
    // 10 seconds of recording + 15 seconds of recording after resume = 25 seconds total
    #expect(diag.recordingDuration == 25)
  }

  @Test("CaptureDiagnostics counts overruns")
  func diagnosticsOverruns() {
    var diag = CaptureDiagnostics()
    diag.record(.init(kind: .bufferOverrun))
    diag.record(.init(kind: .bufferOverrun))
    #expect(diag.overrunCount == 2)
  }
}
