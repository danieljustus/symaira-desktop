@preconcurrency import AVFoundation
import CoreMedia
import Foundation

/// Captures microphone audio as a stream of CMSampleBuffers.
public actor MicrophoneAudioSource {
  private var captureSession: AVCaptureSession?
  private var output: AVCaptureAudioDataOutput?
  private var bufferHandler: (@Sendable (CMSampleBuffer) -> Void)?

  public init() {}

  /// Starts capturing from the given device (or the default input if nil).
  public func start(
    deviceID: String? = nil,
    handler: @escaping @Sendable (CMSampleBuffer) -> Void
  ) throws {
    let session = AVCaptureSession()
    session.beginConfiguration()

    let device: AVCaptureDevice
    if let id = deviceID {
      guard
        let found = AVCaptureDevice.DiscoverySession(
          deviceTypes: [.microphone, .external],
          mediaType: .audio,
          position: .unspecified
        ).devices.first(where: { $0.uniqueID == id })
      else {
        throw CaptureError.sourceNotFound(bundleID: id)
      }
      device = found
    } else {
      guard let defaultDevice = AVCaptureDevice.default(for: .audio) else {
        throw CaptureError.microphoneDenied
      }
      device = defaultDevice
    }

    let input = try AVCaptureDeviceInput(device: device)
    guard session.canAddInput(input) else {
      throw CaptureError.microphoneDenied
    }
    session.addInput(input)

    let dataOutput = AVCaptureAudioDataOutput()
    let delegate = MicrophoneSampleDelegate(handler: handler)
    dataOutput.setSampleBufferDelegate(delegate, queue: DispatchQueue(label: "symmeet.mic"))
    guard session.canAddOutput(dataOutput) else {
      throw CaptureError.trackWriteFailed(reason: "cannot add audio output to session")
    }
    session.addOutput(dataOutput)
    session.commitConfiguration()
    session.startRunning()

    self.captureSession = session
    self.output = dataOutput
    self.bufferHandler = handler
  }

  public func stop() {
    captureSession?.stopRunning()
    captureSession = nil
    output = nil
    bufferHandler = nil
  }
}

// MARK: - CaptureSession seam
//
// Conforms to the internal `MicrophoneCapturing` protocol so CaptureSession
// can drive microphone capture through a protocol-typed source. The existing
// methods already match the protocol requirements (sync methods satisfy the
// async requirements via the concurrency thunk).

extension MicrophoneAudioSource: MicrophoneCapturing {}

// MARK: - Delegate (non-actor, isolated by the serial queue set above)

private final class MicrophoneSampleDelegate: NSObject,
  AVCaptureAudioDataOutputSampleBufferDelegate,
  @unchecked Sendable
{
  private let handler: @Sendable (CMSampleBuffer) -> Void

  init(handler: @escaping @Sendable (CMSampleBuffer) -> Void) {
    self.handler = handler
  }

  func captureOutput(
    _ output: AVCaptureOutput,
    didOutput sampleBuffer: CMSampleBuffer,
    from connection: AVCaptureConnection
  ) {
    handler(sampleBuffer)
  }
}
