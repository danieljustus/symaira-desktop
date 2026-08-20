import CoreMedia
import Foundation
@preconcurrency import ScreenCaptureKit

/// Captures system audio from a ScreenCaptureKit stream.
public actor ScreenAudioSource {
  private var stream: SCStream?
  private var streamOutput: StreamAudioOutput?

  public init() {}

  /// Starts capturing system audio from the given shareable content filter.
  public func start(
    content: SCShareableContent,
    configuration: SCStreamConfiguration,
    handler: @escaping @Sendable (CMSampleBuffer) -> Void
  ) async throws {
    guard !content.displays.isEmpty else {
      throw CaptureError.screenRecordingDenied
    }

    // Capture all system audio; exclude SymMeet from the video (audio-only capture).
    let filter = SCContentFilter(
      display: content.displays[0],
      excludingApplications: [],
      exceptingWindows: [])

    let cfg = SCStreamConfiguration()
    cfg.capturesAudio = true
    cfg.excludesCurrentProcessAudio = true
    cfg.sampleRate = configuration.sampleRate
    cfg.channelCount = configuration.channelCount

    let output = StreamAudioOutput(handler: handler)
    let stream = SCStream(filter: filter, configuration: cfg, delegate: nil)
    try stream.addStreamOutput(
      output,
      type: .audio,
      sampleHandlerQueue: DispatchQueue(label: "symmeet.screenaudio")
    )
    try await stream.startCapture()

    self.stream = stream
    self.streamOutput = output
  }

  public func stop() async throws {
    guard let stream else { return }
    try await stream.stopCapture()
    self.stream = nil
    self.streamOutput = nil
  }
}

// MARK: - CaptureSession seam
//
// Conforms to the internal `SystemAudioCapturing` protocol so CaptureSession
// can drive system-audio capture through a protocol-typed source. The
// shareable-content fetch and stream configuration live here; runtime
// behavior is unchanged (same API, same values as the previous inline code).

extension ScreenAudioSource: SystemAudioCapturing {
  func start(
    handler: @escaping @Sendable (CMSampleBuffer) -> Void
  ) async throws {
    let content = try await SCShareableContent.excludingDesktopWindows(
      true,
      onScreenWindowsOnly: false
    )
    let configuration = SCStreamConfiguration()
    configuration.sampleRate = 48000
    configuration.channelCount = 2
    try await start(content: content, configuration: configuration, handler: handler)
  }
}

// MARK: - SCStreamOutput delegate

private final class StreamAudioOutput: NSObject, SCStreamOutput, @unchecked Sendable {
  private let handler: @Sendable (CMSampleBuffer) -> Void

  init(handler: @escaping @Sendable (CMSampleBuffer) -> Void) {
    self.handler = handler
  }

  func stream(
    _ stream: SCStream,
    didOutputSampleBuffer sampleBuffer: CMSampleBuffer,
    of type: SCStreamOutputType
  ) {
    guard type == .audio else { return }
    handler(sampleBuffer)
  }
}
