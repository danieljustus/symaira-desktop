@preconcurrency import AVFoundation
import CoreMedia
import Foundation
@preconcurrency import ScreenCaptureKit
import SymMeetCore

/// @unchecked Sendable wrapper for CMSampleBuffer.
/// CMSampleBuffer is not Sendable by default; this wrapper is safe because
/// the buffer is consumed immediately on the actor's executor and not shared.
struct SendableSampleBuffer: @unchecked Sendable {
  let buffer: CMSampleBuffer
}

/// Configuration for a capture session.
public struct CaptureSessionConfiguration: Sendable {
  public enum SystemAudioSource: Sendable {
    case allOutputs
    case application(bundleID: String)
    case disabled
  }

  public enum MicrophoneSource: Sendable {
    case defaultDevice
    case device(id: String)
    case disabled
  }

  public let sessionID: UUID
  public let authorization: ConsentRecord
  public let systemAudio: SystemAudioSource
  public let microphone: MicrophoneSource
  public let outputDirectory: URL

  public init(
    sessionID: UUID,
    authorization: ConsentRecord,
    systemAudio: SystemAudioSource = .allOutputs,
    microphone: MicrophoneSource = .defaultDevice,
    outputDirectory: URL
  ) {
    self.sessionID = sessionID
    self.authorization = authorization
    self.systemAudio = systemAudio
    self.microphone = microphone
    self.outputDirectory = outputDirectory
  }
}

/// Result of a completed (or interrupted) capture session.
public struct CaptureResult: Sendable {
  public let sessionID: UUID
  public let systemTrackURL: URL?
  public let microphoneTrackURL: URL?
  public let diagnostics: CaptureDiagnostics
  public let isComplete: Bool

  public init(
    sessionID: UUID,
    systemTrackURL: URL?,
    microphoneTrackURL: URL?,
    diagnostics: CaptureDiagnostics,
    isComplete: Bool
  ) {
    self.sessionID = sessionID
    self.systemTrackURL = systemTrackURL
    self.microphoneTrackURL = microphoneTrackURL
    self.diagnostics = diagnostics
    self.isComplete = isComplete
  }
}

// MARK: - Capture source seams
//
// Internal protocols that let tests inject fake sources so the session's
// lifecycle, buffer routing, and error handling can be exercised without
// touching real capture hardware or ScreenCaptureKit.

internal protocol SystemAudioCapturing: AnyObject, Sendable {
  func start(handler: @escaping @Sendable (CMSampleBuffer) -> Void) async throws
  func stop() async throws
}

internal protocol MicrophoneCapturing: AnyObject, Sendable {
  func start(
    deviceID: String?,
    handler: @escaping @Sendable (CMSampleBuffer) -> Void
  ) async throws
  func stop() async
}

/// The main capture session: coordinates system-audio and microphone capture
/// with synchronized timestamps, bounded buffers, and recoverable artifacts.
public actor CaptureSession {
  private var stateMachine = CaptureStateMachine()
  private var config: CaptureSessionConfiguration?
  private let clock = ClockSynchronizer()
  private let screenSource: any SystemAudioCapturing
  private let micSource: any MicrophoneCapturing
  private var systemWriter: TrackWriter?
  private var micWriter: TrackWriter?
  private var diagnostics = CaptureDiagnostics()
  private var pauseStartTime: CMTime?

  public init() {
    self.screenSource = ScreenAudioSource()
    self.micSource = MicrophoneAudioSource()
  }

  /// Test seam: injects fake capture sources so the session can be driven
  /// without real hardware. Internal only; never used by production callers.
  internal init(
    systemSource: any SystemAudioCapturing,
    micSource: any MicrophoneCapturing
  ) {
    self.screenSource = systemSource
    self.micSource = micSource
  }

  /// Current state of the session.
  public var state: CaptureState { stateMachine.state }

  // MARK: - Session lifecycle

  /// Starts recording with the given configuration.
  /// Throws if the authorization is invalid or a session is already active.
  public func start(configuration: CaptureSessionConfiguration) async throws {
    guard stateMachine.transition(to: .authorizing) else {
      throw CaptureError.captureSessionAlreadyActive
    }

    self.config = configuration

    // Prepare track writers
    let systemURL = configuration.outputDirectory.appending(
      path: "system-audio.caf", directoryHint: .notDirectory)
    let micURL = configuration.outputDirectory.appending(
      path: "microphone.caf", directoryHint: .notDirectory)

    try FileManager.default.createDirectory(
      at: configuration.outputDirectory, withIntermediateDirectories: true)
    try FileManager.default.setAttributes(
      [.posixPermissions: 0o700], ofItemAtPath: configuration.outputDirectory.path)

    let sysWriter = TrackWriter(url: systemURL)
    let micW = TrackWriter(url: micURL)
    let defaultFormat = AVAudioFormat(
      standardFormatWithSampleRate: 48000, channels: 2)!
    try await sysWriter.prepare(format: defaultFormat)
    try await micW.prepare(format: defaultFormat)

    self.systemWriter = sysWriter
    self.micWriter = micW

    guard stateMachine.transition(to: .starting) else {
      throw CaptureError.captureSessionAlreadyActive
    }

    // Start system audio capture
    if case .disabled = configuration.systemAudio {
      // skip
    } else {
      try await screenSource.start { [weak self] buffer in
        let wrapped = SendableSampleBuffer(buffer: buffer)
        Task { await self?.handleSystemBuffer(wrapped) }
      }
    }

    // Start microphone capture
    switch configuration.microphone {
    case .defaultDevice:
      try await micSource.start(deviceID: nil) { [weak self] buffer in
        let wrapped = SendableSampleBuffer(buffer: buffer)
        Task { await self?.handleMicBuffer(wrapped) }
      }
    case .device(let id):
      try await micSource.start(deviceID: id) { [weak self] buffer in
        let wrapped = SendableSampleBuffer(buffer: buffer)
        Task { await self?.handleMicBuffer(wrapped) }
      }
    case .disabled:
      break
    }

    stateMachine.transition(to: .recording)
    diagnostics.record(.init(kind: .started))
  }

  /// Pauses capture; gap is recorded in the timeline.
  public func pause() async throws {
    guard stateMachine.transition(to: .pausing) else {
      throw CaptureError.captureSessionNotActive
    }
    pauseStartTime = CMClockGetTime(CMClockGetHostTimeClock())
    stateMachine.transition(to: .paused)
    diagnostics.record(.init(kind: .paused))
  }

  /// Resumes capture after a pause.
  public func resume() async throws {
    guard stateMachine.transition(to: .starting) else {
      throw CaptureError.captureSessionNotActive
    }
    if let pauseStart = pauseStartTime {
      let now = CMClockGetTime(CMClockGetHostTimeClock())
      let duration = CMTimeSubtract(now, pauseStart)
      await clock.recordPause(duration: duration)
      pauseStartTime = nil
    }
    stateMachine.transition(to: .recording)
    diagnostics.record(.init(kind: .resumed))
  }

  /// Stops capture and finalizes track files.
  public func stop() async throws -> CaptureResult {
    guard stateMachine.transition(to: .stopping) else {
      throw CaptureError.captureSessionNotActive
    }
    diagnostics.record(.init(kind: .stopped))

    // Stop capture sources
    try? await screenSource.stop()
    await micSource.stop()

    // Finalize writers
    await systemWriter?.finalize()
    await micWriter?.finalize()

    stateMachine.transition(to: .finished)

    let sysURL = await systemWriter?.outputURL
    let micURL = await micWriter?.outputURL

    let result = CaptureResult(
      sessionID: config?.sessionID ?? UUID(),
      systemTrackURL: sysURL,
      microphoneTrackURL: micURL,
      diagnostics: diagnostics,
      isComplete: true
    )
    return result
  }

  /// Signals an external interruption (device lost, sleep/wake).
  public func interrupt(reason: String) {
    guard stateMachine.transition(to: .interrupted(reason: reason)) else { return }
    diagnostics.record(.init(kind: .interrupted, detail: reason))
  }

  // MARK: - Buffer handling

  private func handleSystemBuffer(_ wrapped: SendableSampleBuffer) async {
    guard case .recording = stateMachine.state, let writer = systemWriter else { return }
    let pts = CMSampleBufferGetPresentationTimeStamp(wrapped.buffer)
    guard let sessionTime = await clock.sessionTime(for: pts) else { return }
    do {
      try await writer.append(wrapped.buffer, sessionTime: sessionTime)
    } catch CaptureError.bufferOverrun {
      diagnostics.record(.init(kind: .bufferOverrun, detail: "system"))
      stateMachine.transition(to: .failed(reason: "Buffer overrun on system track"))
    } catch {
      diagnostics.record(.init(kind: .trackWriteFailed, detail: error.localizedDescription))
    }
  }

  private func handleMicBuffer(_ wrapped: SendableSampleBuffer) async {
    guard case .recording = stateMachine.state, let writer = micWriter else { return }
    let pts = CMSampleBufferGetPresentationTimeStamp(wrapped.buffer)
    guard let sessionTime = await clock.sessionTime(for: pts) else { return }
    do {
      try await writer.append(wrapped.buffer, sessionTime: sessionTime)
    } catch CaptureError.bufferOverrun {
      diagnostics.record(.init(kind: .bufferOverrun, detail: "microphone"))
      stateMachine.transition(to: .failed(reason: "Buffer overrun on microphone track"))
    } catch {
      diagnostics.record(.init(kind: .trackWriteFailed, detail: error.localizedDescription))
    }
  }
}
