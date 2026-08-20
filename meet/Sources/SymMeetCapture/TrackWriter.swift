@preconcurrency import AVFoundation
import CoreMedia
import Foundation

/// A gap event recorded in the session timeline.
public struct CaptureGap: Sendable {
  public enum Reason: String, Sendable {
    case pause
    case deviceLoss
    case sleepWake
    case sourceInterruption
  }

  public let reason: Reason
  public let startOffset: CMTime
  public let endOffset: CMTime?
}

/// A bounded, async writer that receives linear PCM CMSampleBuffers and
/// appends them incrementally to a CAF audio file.
public actor TrackWriter {
  /// Maximum samples that may be enqueued before signaling buffer overrun.
  private static let maxBufferedSamples = 2048

  private let url: URL
  private var writer: AVAssetWriter?
  private var input: AVAssetWriterInput?
  private var sampleCount: Int = 0
  private var hasStartedSession = false
  public private(set) var gaps: [CaptureGap] = []

  public init(url: URL) {
    self.url = url
  }

  // MARK: - Lifecycle

  /// Prepares the underlying AVAssetWriter for the given PCM format.
  public func prepare(format: AVAudioFormat) throws {
    let writer = try AVAssetWriter(outputURL: url, fileType: .caf)
    let settings: [String: Any] = [
      AVFormatIDKey: kAudioFormatLinearPCM,
      AVSampleRateKey: format.sampleRate,
      AVNumberOfChannelsKey: format.channelCount,
      AVLinearPCMBitDepthKey: 32,
      AVLinearPCMIsFloatKey: true,
      AVLinearPCMIsBigEndianKey: false,
      AVLinearPCMIsNonInterleaved: false,
    ]
    let input = AVAssetWriterInput(mediaType: .audio, outputSettings: settings)
    input.expectsMediaDataInRealTime = true
    writer.add(input)
    guard writer.startWriting() else {
      throw CaptureError.trackWriteFailed(reason: writer.error?.localizedDescription ?? "unknown")
    }
    self.writer = writer
    self.input = input
  }

  /// Appends a sample buffer. Throws `.bufferOverrun` if the queue is too large.
  public func append(_ sampleBuffer: CMSampleBuffer, sessionTime: CMTime) throws {
    guard let input else { return }
    guard sampleCount < Self.maxBufferedSamples else {
      throw CaptureError.bufferOverrun
    }
    if !hasStartedSession {
      writer?.startSession(atSourceTime: sessionTime)
      hasStartedSession = true
    }
    if input.isReadyForMoreMediaData {
      input.append(sampleBuffer)
      sampleCount += 1
    }
  }

  /// Records a timeline gap at the given session-relative offset.
  public func recordGapStart(reason: CaptureGap.Reason, at offset: CMTime) {
    gaps.append(CaptureGap(reason: reason, startOffset: offset, endOffset: nil))
  }

  /// Closes the most recent gap.
  public func closeLastGap(at endOffset: CMTime) {
    guard let last = gaps.last, last.endOffset == nil else { return }
    gaps[gaps.count - 1] = CaptureGap(
      reason: last.reason,
      startOffset: last.startOffset,
      endOffset: endOffset
    )
  }

  /// Finalizes the writer, flushing all pending samples to disk.
  public func finalize() async {
    input?.markAsFinished()
    await writer?.finishWriting()
    sampleCount = 0
  }

  /// URL of the output file.
  public var outputURL: URL { url }
}
