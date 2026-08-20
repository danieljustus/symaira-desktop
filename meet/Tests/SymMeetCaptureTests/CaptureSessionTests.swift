import CoreMedia
import Foundation
import SymMeetCore
import Testing

@testable import SymMeetCapture

// MARK: - Fakes

/// A fake system-audio source that records calls and can emit buffers through
/// the handler it was started with.
private actor FakeSystemSource: SystemAudioCapturing {
  private var startCount = 0
  private var stopCount = 0
  private var handler: (@Sendable (CMSampleBuffer) -> Void)?
  private var startError: Error?

  func start(handler: @escaping @Sendable (CMSampleBuffer) -> Void) async throws {
    startCount += 1
    self.handler = handler
    if let startError { throw startError }
  }

  func stop() async throws {
    stopCount += 1
  }

  func setStartError(_ error: Error?) {
    startError = error
  }

  /// Delivers a buffer through the handler captured at start (if any).
  func emit(_ wrapped: SendableSampleBuffer) {
    handler?(wrapped.buffer)
  }

  func numberOfStarts() -> Int { startCount }
  func numberOfStops() -> Int { stopCount }
}

/// A fake microphone source; mirrors ``FakeSystemSource``.
private actor FakeMicSource: MicrophoneCapturing {
  private var startCount = 0
  private var stopCount = 0
  private var startedDeviceIDs: [String?] = []
  private var handler: (@Sendable (CMSampleBuffer) -> Void)?
  private var startError: Error?

  func start(
    deviceID: String?,
    handler: @escaping @Sendable (CMSampleBuffer) -> Void
  ) async throws {
    startCount += 1
    startedDeviceIDs.append(deviceID)
    self.handler = handler
    if let startError { throw startError }
  }

  func stop() async {
    stopCount += 1
  }

  func setStartError(_ error: Error?) {
    startError = error
  }

  func emit(_ wrapped: SendableSampleBuffer) {
    handler?(wrapped.buffer)
  }

  func numberOfStarts() -> Int { startCount }
  func numberOfStops() -> Int { stopCount }
  func deviceIDs() -> [String?] { startedDeviceIDs }
}

/// Interactive authorizer that always attests, so tests can mint a valid
/// `ConsentRecord` through the public `RecordingAuthorization` API.
private struct AttestingAuthorizer: InteractiveRecordingAuthorizer {
  func requestAuthorization(
    for request: RecordingAuthorizationRequest
  ) async throws -> InteractiveAuthorizationDecision {
    InteractiveAuthorizationDecision(
      operatorAttested: true,
      noticeAt: Date(timeIntervalSince1970: 0),
      scope: request.scope,
      expiresAt: Date(timeIntervalSince1970: 2_000_000_000)
    )
  }
}

// MARK: - Test support

@Suite("CaptureSession (fake sources)")
struct CaptureSessionTests {

  /// A valid consent record minted through the public authorization path.
  private func consentRecord(sessionID: UUID) async throws -> ConsentRecord {
    let authorization = RecordingAuthorization(authorizer: AttestingAuthorizer())
    return try await authorization.requestAuthorization(
      sessionID: sessionID,
      scope: RecordingScope(meetingID: UUID(), purpose: "test")
    )
  }

  private func makeConfiguration(
    sessionID: UUID,
    systemAudio: CaptureSessionConfiguration.SystemAudioSource = .allOutputs,
    microphone: CaptureSessionConfiguration.MicrophoneSource = .defaultDevice
  ) async throws -> (CaptureSessionConfiguration, URL) {
    let outputDirectory = FileManager.default.temporaryDirectory.appending(
      path: "capture-session-tests-\(UUID().uuidString)", directoryHint: .isDirectory)
    try FileManager.default.createDirectory(
      at: outputDirectory, withIntermediateDirectories: true)
    let record = try await consentRecord(sessionID: sessionID)
    let config = CaptureSessionConfiguration(
      sessionID: sessionID,
      authorization: record,
      systemAudio: systemAudio,
      microphone: microphone,
      outputDirectory: outputDirectory
    )
    return (config, outputDirectory)
  }

  /// Creates a single-frame PCM sample buffer with a valid format description,
  /// so buffer routing can be exercised without capture hardware.
  private func makeSampleBuffer() throws -> CMSampleBuffer {
    var asbd = AudioStreamBasicDescription(
      mSampleRate: 48_000,
      mFormatID: kAudioFormatLinearPCM,
      mFormatFlags: kAudioFormatFlagIsFloat | kAudioFormatFlagIsPacked,
      mBytesPerPacket: 8,
      mFramesPerPacket: 1,
      mBytesPerFrame: 8,
      mChannelsPerFrame: 2,
      mBitsPerChannel: 32,
      mReserved: 0
    )
    var formatDescription: CMAudioFormatDescription?
    let formatStatus = CMAudioFormatDescriptionCreate(
      allocator: kCFAllocatorDefault,
      asbd: &asbd,
      layoutSize: 0,
      layout: nil,
      magicCookieSize: 0,
      magicCookie: nil,
      extensions: nil,
      formatDescriptionOut: &formatDescription
    )
    #expect(formatStatus == noErr)

    var blockBuffer: CMBlockBuffer?
    let blockStatus = CMBlockBufferCreateWithMemoryBlock(
      allocator: kCFAllocatorDefault,
      memoryBlock: nil,
      blockLength: 8,
      blockAllocator: nil,
      customBlockSource: nil,
      offsetToData: 0,
      dataLength: 8,
      flags: 0,
      blockBufferOut: &blockBuffer
    )
    #expect(blockStatus == noErr)

    var timing = CMSampleTimingInfo(
      duration: CMTime(value: 1, timescale: 48_000),
      presentationTimeStamp: CMTime(value: 48_000, timescale: 48_000),
      decodeTimeStamp: .invalid
    )
    var sampleBuffer: CMSampleBuffer?
    let sampleStatus = CMSampleBufferCreate(
      allocator: kCFAllocatorDefault,
      dataBuffer: blockBuffer,
      dataReady: true,
      makeDataReadyCallback: nil,
      refcon: nil,
      formatDescription: formatDescription,
      sampleCount: 1,
      sampleTimingEntryCount: 1,
      sampleTimingArray: &timing,
      sampleSizeEntryCount: 0,
      sampleSizeArray: nil,
      sampleBufferOut: &sampleBuffer
    )
    #expect(sampleStatus == noErr)
    return try #require(sampleBuffer)
  }

  // MARK: - Lifecycle

  @Test("start drives both sources and reaches recording")
  func startReachesRecording() async throws {
    let sessionID = UUID()
    let (config, outputDirectory) = try await makeConfiguration(sessionID: sessionID)
    defer { try? FileManager.default.removeItem(at: outputDirectory) }

    let systemSource = FakeSystemSource()
    let micSource = FakeMicSource()
    let session = CaptureSession(systemSource: systemSource, micSource: micSource)

    try await session.start(configuration: config)

    #expect(await session.state == .recording)
    #expect(await systemSource.numberOfStarts() == 1)
    #expect(await micSource.numberOfStarts() == 1)
    #expect(await micSource.deviceIDs() == [nil])
    // Track files are prepared under the output directory.
    #expect(
      FileManager.default.fileExists(
        atPath: outputDirectory.appending(path: "system-audio.caf").path))
    #expect(
      FileManager.default.fileExists(
        atPath: outputDirectory.appending(path: "microphone.caf").path))

    _ = try await session.stop()
  }

  @Test("disabled sources are not started")
  func disabledSourcesAreSkipped() async throws {
    let sessionID = UUID()
    let (config, outputDirectory) = try await makeConfiguration(
      sessionID: sessionID,
      systemAudio: .disabled,
      microphone: .disabled
    )
    defer { try? FileManager.default.removeItem(at: outputDirectory) }

    let systemSource = FakeSystemSource()
    let micSource = FakeMicSource()
    let session = CaptureSession(systemSource: systemSource, micSource: micSource)

    try await session.start(configuration: config)

    #expect(await session.state == .recording)
    #expect(await systemSource.numberOfStarts() == 0)
    #expect(await micSource.numberOfStarts() == 0)

    _ = try await session.stop()
  }

  @Test("explicit microphone device ID is forwarded")
  func deviceIDIsForwarded() async throws {
    let sessionID = UUID()
    let (config, outputDirectory) = try await makeConfiguration(
      sessionID: sessionID,
      microphone: .device(id: "builtin-mic-42")
    )
    defer { try? FileManager.default.removeItem(at: outputDirectory) }

    let systemSource = FakeSystemSource()
    let micSource = FakeMicSource()
    let session = CaptureSession(systemSource: systemSource, micSource: micSource)

    try await session.start(configuration: config)

    #expect(await micSource.deviceIDs() == ["builtin-mic-42"])

    _ = try await session.stop()
  }

  @Test("start twice throws captureSessionAlreadyActive")
  func doubleStartThrows() async throws {
    let sessionID = UUID()
    let (config, outputDirectory) = try await makeConfiguration(sessionID: sessionID)
    defer { try? FileManager.default.removeItem(at: outputDirectory) }

    let session = CaptureSession(
      systemSource: FakeSystemSource(), micSource: FakeMicSource())

    try await session.start(configuration: config)
    await #expect(throws: CaptureError.captureSessionAlreadyActive) {
      try await session.start(configuration: config)
    }
    _ = try await session.stop()
  }

  @Test("stop before start throws captureSessionNotActive")
  func stopBeforeStartThrows() async {
    let session = CaptureSession(
      systemSource: FakeSystemSource(), micSource: FakeMicSource())
    await #expect(throws: CaptureError.captureSessionNotActive) {
      _ = try await session.stop()
    }
  }

  @Test("pause and resume transition states and stop sources once")
  func pauseResumeStop() async throws {
    let sessionID = UUID()
    let (config, outputDirectory) = try await makeConfiguration(sessionID: sessionID)
    defer { try? FileManager.default.removeItem(at: outputDirectory) }

    let systemSource = FakeSystemSource()
    let micSource = FakeMicSource()
    let session = CaptureSession(systemSource: systemSource, micSource: micSource)

    try await session.start(configuration: config)
    try await session.pause()
    #expect(await session.state == .paused)
    try await session.resume()
    #expect(await session.state == .recording)

    let result = try await session.stop()
    #expect(await session.state == .finished)
    #expect(result.isComplete)
    #expect(await systemSource.numberOfStops() == 1)
    #expect(await micSource.numberOfStops() == 1)
  }

  @Test("interrupt transitions to interrupted and records diagnostics")
  func interruptTransitions() async throws {
    let sessionID = UUID()
    let (config, outputDirectory) = try await makeConfiguration(sessionID: sessionID)
    defer { try? FileManager.default.removeItem(at: outputDirectory) }

    let session = CaptureSession(
      systemSource: FakeSystemSource(), micSource: FakeMicSource())

    try await session.start(configuration: config)
    await session.interrupt(reason: "device lost")
    #expect(await session.state == .interrupted(reason: "device lost"))
  }

  @Test("source start failure propagates")
  func sourceStartFailurePropagates() async throws {
    let sessionID = UUID()
    let (config, outputDirectory) = try await makeConfiguration(sessionID: sessionID)
    defer { try? FileManager.default.removeItem(at: outputDirectory) }

    let systemSource = FakeSystemSource()
    await systemSource.setStartError(
      CaptureError.sourceNotFound(bundleID: "com.example.missing"))
    let session = CaptureSession(systemSource: systemSource, micSource: FakeMicSource())

    await #expect(throws: CaptureError.self) {
      try await session.start(configuration: config)
    }
  }

  // MARK: - Buffer routing

  @Test("buffers emitted while recording are routed without crashing")
  func bufferRouting() async throws {
    let sessionID = UUID()
    let (config, outputDirectory) = try await makeConfiguration(sessionID: sessionID)
    defer { try? FileManager.default.removeItem(at: outputDirectory) }

    let systemSource = FakeSystemSource()
    let micSource = FakeMicSource()
    let session = CaptureSession(systemSource: systemSource, micSource: micSource)

    try await session.start(configuration: config)
    let buffer = SendableSampleBuffer(buffer: try makeSampleBuffer())

    await systemSource.emit(buffer)
    await micSource.emit(buffer)
    // Give the actor-routed handler tasks a moment to run.
    try await Task.sleep(for: .milliseconds(100))

    #expect(await session.state == .recording)
    _ = try await session.stop()
    #expect(await session.state == .finished)
  }
}
