@preconcurrency import AVFoundation
import Foundation
import SymDictationCore

/// Upper bound so a forgotten hands-free session cannot grow without limit (10 minutes).
private let dictationMaximumSamples = 16_000 * 600

enum DictationRecorderError: Error, LocalizedError {
  case microphoneNotAuthorized
  case noInputDevice
  case engineFailed(String)

  var errorDescription: String? {
    switch self {
    case .microphoneNotAuthorized:
      return
        "Der Zugriff auf das Mikrofon fehlt. Bitte in den Systemeinstellungen unter Datenschutz & Sicherheit freigeben."
    case .noInputDevice:
      return "Es ist kein Mikrofon verfügbar."
    case .engineFailed(let reason):
      return "Die Aufnahme konnte nicht gestartet werden: \(reason)"
    }
  }
}

/// Records microphone audio into memory while dictation is active.
///
/// The engine exists only between `start()` and `finish()`/`cancel()`, so the microphone is
/// never open outside a held key or a hands-free session. Nothing is written to disk.
@MainActor
final class DictationMicRecorder: DictationRecorder {
  private var engine: AVAudioEngine?
  private let buffer = SampleBuffer()

  func start() throws {
    guard AVCaptureDevice.authorizationStatus(for: .audio) == .authorized else {
      throw DictationRecorderError.microphoneNotAuthorized
    }
    cancel()
    buffer.reset()

    let engine = AVAudioEngine()
    let input = engine.inputNode
    let inputFormat = input.outputFormat(forBus: 0)
    guard inputFormat.sampleRate > 0, inputFormat.channelCount > 0 else {
      throw DictationRecorderError.noInputDevice
    }
    guard
      let targetFormat = AVAudioFormat(
        commonFormat: .pcmFormatFloat32, sampleRate: 16_000, channels: 1, interleaved: false),
      let converter = AVAudioConverter(from: inputFormat, to: targetFormat)
    else {
      throw DictationRecorderError.engineFailed("Audioformat wird nicht unterstützt")
    }

    let sink = buffer
    input.installTap(onBus: 0, bufferSize: 2048, format: inputFormat) { pcm, _ in
      sink.append(converting: pcm, with: converter, to: targetFormat)
    }
    do {
      try engine.start()
    } catch {
      input.removeTap(onBus: 0)
      throw DictationRecorderError.engineFailed(error.localizedDescription)
    }
    self.engine = engine
  }

  func finish() -> [Float] {
    stopEngine()
    return buffer.take()
  }

  func cancel() {
    stopEngine()
    buffer.reset()
  }

  private func stopEngine() {
    guard let engine else { return }
    engine.inputNode.removeTap(onBus: 0)
    engine.stop()
    self.engine = nil
  }
}

/// Thread-safe sample accumulator written from the audio thread.
private final class SampleBuffer: @unchecked Sendable {
  private let lock = NSLock()
  private var samples: [Float] = []

  func reset() {
    lock.lock()
    samples.removeAll(keepingCapacity: false)
    lock.unlock()
  }

  func take() -> [Float] {
    lock.lock()
    defer { lock.unlock() }
    let result = samples
    samples.removeAll(keepingCapacity: false)
    return result
  }

  func append(
    converting pcm: AVAudioPCMBuffer, with converter: AVAudioConverter, to format: AVAudioFormat
  ) {
    let ratio = format.sampleRate / pcm.format.sampleRate
    let capacity = AVAudioFrameCount(Double(pcm.frameLength) * ratio) + 32
    guard let out = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: capacity) else { return }
    var supplied = false
    var error: NSError?
    converter.convert(to: out, error: &error) { _, status in
      if supplied {
        status.pointee = .noDataNow
        return nil
      }
      supplied = true
      status.pointee = .haveData
      return pcm
    }
    guard error == nil, let channel = out.floatChannelData?[0], out.frameLength > 0 else { return }
    lock.lock()
    defer { lock.unlock() }
    let room = dictationMaximumSamples - samples.count
    guard room > 0 else { return }
    samples.append(
      contentsOf: UnsafeBufferPointer(start: channel, count: min(Int(out.frameLength), room)))
  }
}
