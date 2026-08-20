@preconcurrency import AVFoundation
import Foundation

public struct AudioSampleChunk: Equatable, Sendable {
  public let samples: [Float]
  public let startMS: Int
  public let endMS: Int

  public init(samples: [Float], startMS: Int, endMS: Int) {
    self.samples = samples
    self.startMS = startMS
    self.endMS = endMS
  }
}

public struct AudioSampleReader: Sendable {
  public static let outputSampleRate = 16_000.0

  private let url: URL
  private let outputChunkFrames: AVAudioFrameCount

  public init(url: URL, outputChunkDuration: TimeInterval = 1.0) {
    self.url = url
    outputChunkFrames = max(
      1, AVAudioFrameCount((outputChunkDuration * Self.outputSampleRate).rounded()))
  }

  public func chunks() -> AsyncThrowingStream<AudioSampleChunk, Error> {
    AsyncThrowingStream { continuation in
      let worker = AudioSampleReaderWorker(url: url, outputChunkFrames: outputChunkFrames)
      let task = Task {
        do {
          try worker.read { chunk in
            continuation.yield(chunk)
          }
          continuation.finish()
        } catch is CancellationError {
          continuation.finish(throwing: AudioError.cancelled)
        } catch let error as AudioError {
          continuation.finish(throwing: error)
        } catch {
          continuation.finish(throwing: AudioError.invalidAudioFormat)
        }
      }
      continuation.onTermination = { _ in task.cancel() }
    }
  }
}

private final class AudioSampleReaderWorker: @unchecked Sendable {
  private let url: URL
  private let outputChunkFrames: AVAudioFrameCount

  init(url: URL, outputChunkFrames: AVAudioFrameCount) {
    self.url = url
    self.outputChunkFrames = outputChunkFrames
  }

  func read(yield: (AudioSampleChunk) -> Void) throws {
    let file: AVAudioFile
    do {
      file = try AVAudioFile(forReading: url)
    } catch {
      throw AudioError.unsupportedCodec
    }

    let inputFormat = file.processingFormat
    guard
      let outputFormat = AVAudioFormat(
        commonFormat: .pcmFormatFloat32,
        sampleRate: AudioSampleReader.outputSampleRate,
        channels: 1,
        interleaved: false),
      let converter = AVAudioConverter(from: inputFormat, to: outputFormat)
    else {
      throw AudioError.invalidAudioFormat
    }

    let inputCapacity = max(
      AVAudioFrameCount(1024),
      AVAudioFrameCount(
        (Double(outputChunkFrames) * inputFormat.sampleRate / AudioSampleReader.outputSampleRate + 1)
          .rounded(.up)))
    var inputEnded = false
    var outputPosition: AVAudioFramePosition = 0

    while true {
      try Task.checkCancellation()
      guard
        let outputBuffer = AVAudioPCMBuffer(
          pcmFormat: outputFormat, frameCapacity: outputChunkFrames)
      else {
        throw AudioError.invalidAudioFormat
      }

      var conversionError: NSError?
      var inputReadError: Error?
      let status = converter.convert(to: outputBuffer, error: &conversionError) { _, inputStatus in
        if inputEnded {
          inputStatus.pointee = .endOfStream
          return nil
        }

        guard
          let inputBuffer = AVAudioPCMBuffer(
            pcmFormat: inputFormat, frameCapacity: inputCapacity)
        else {
          inputStatus.pointee = .endOfStream
          inputEnded = true
          return nil
        }

        do {
          let remainingFrames = file.length - file.framePosition
          guard remainingFrames > 0 else {
            inputStatus.pointee = .endOfStream
            inputEnded = true
            return nil
          }
          let frameCount = min(inputCapacity, AVAudioFrameCount(remainingFrames))
          try file.read(into: inputBuffer, frameCount: frameCount)
        } catch {
          inputReadError = error
          inputStatus.pointee = .endOfStream
          inputEnded = true
          return nil
        }

        if inputBuffer.frameLength == 0 {
          inputStatus.pointee = .endOfStream
          inputEnded = true
          return nil
        }

        inputStatus.pointee = .haveData
        return inputBuffer
      }

      if let inputReadError { throw inputReadError }
      if let conversionError { throw conversionError }
      if status == .error { throw AudioError.invalidAudioFormat }

      let frameCount = Int(outputBuffer.frameLength)
      if frameCount > 0, let channel = outputBuffer.floatChannelData?.pointee {
        let samples = Array(UnsafeBufferPointer(start: channel, count: frameCount))
        let startMS = Int(
          (Double(outputPosition) / AudioSampleReader.outputSampleRate * 1_000).rounded())
        outputPosition += AVAudioFramePosition(frameCount)
        let endMS = Int(
          (Double(outputPosition) / AudioSampleReader.outputSampleRate * 1_000).rounded())
        yield(AudioSampleChunk(samples: samples, startMS: startMS, endMS: endMS))
      }

      if status == .endOfStream { break }
    }
  }
}
