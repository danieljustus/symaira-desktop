@preconcurrency import AVFoundation
import CryptoKit
import Foundation

public struct AudioAssetLimits: Equatable, Sendable {
  public let maxBytes: Int64
  public let maxDuration: TimeInterval

  public init(maxBytes: Int64 = 4_000_000_000, maxDuration: TimeInterval = 24 * 60 * 60) {
    self.maxBytes = maxBytes
    self.maxDuration = maxDuration
  }
}

public struct AudioAssetProbe: Sendable {
  public let limits: AudioAssetLimits

  public init(limits: AudioAssetLimits = AudioAssetLimits()) {
    self.limits = limits
  }

  public func probe(_ url: URL, allowedRoot: URL? = nil) async throws -> AudioAsset {
    let resolvedURL = try validateLocalURL(url, allowedRoot: allowedRoot)
    let values = try resolvedURL.resourceValues(forKeys: [.isDirectoryKey, .fileSizeKey])
    guard values.isDirectory != true else { throw AudioError.directoryNotAllowed }
    guard FileManager.default.fileExists(atPath: resolvedURL.path) else {
      throw AudioError.missingFile
    }

    let byteSize = Int64(values.fileSize ?? 0)
    guard byteSize > 0 else { throw AudioError.zeroLength }
    guard byteSize <= limits.maxBytes else { throw AudioError.exceedsByteLimit }

    let container = resolvedURL.pathExtension.lowercased()
    guard ["wav", "aif", "aiff", "m4a", "mp4", "mov", "mp3", "flac"].contains(container)
    else {
      throw AudioError.unsupportedContainer(container.isEmpty ? "unknown" : container)
    }

    let asset = AVURLAsset(url: resolvedURL)
    do {
      guard try await asset.load(.isPlayable) else { throw AudioError.unsupportedCodec }
      guard !(try await asset.load(.hasProtectedContent)) else { throw AudioError.protectedMedia }

      let duration = try await asset.load(.duration)
      let durationSeconds = duration.isNumeric ? duration.seconds : 0
      guard durationSeconds > 0 else { throw AudioError.zeroLength }
      guard durationSeconds <= limits.maxDuration else { throw AudioError.exceedsDurationLimit }

      let tracks = try await asset.loadTracks(withMediaType: .audio)
      guard let track = tracks.first else { throw AudioError.missingAudioTrack }
      let descriptions = try await track.load(.formatDescriptions)
      guard
        let description = descriptions.first,
        let streamDescription = CMAudioFormatDescriptionGetStreamBasicDescription(description)?
          .pointee,
        streamDescription.mSampleRate > 0,
        streamDescription.mChannelsPerFrame > 0
      else {
        throw AudioError.unsupportedCodec
      }

      return AudioAsset(
        sourceURL: resolvedURL,
        metadata: SourceAssetMetadata(
          container: container,
          durationMS: Int((durationSeconds * 1_000).rounded()),
          channelCount: Int(streamDescription.mChannelsPerFrame),
          sampleRate: streamDescription.mSampleRate,
          byteSize: byteSize,
          sha256: try sha256(of: resolvedURL)
        ),
        audioTrackIndex: 0
      )
    } catch let error as AudioError {
      throw error
    } catch {
      throw AudioError.unsupportedCodec
    }
  }

  private func validateLocalURL(_ url: URL, allowedRoot: URL?) throws -> URL {
    guard url.isFileURL else { throw AudioError.notLocalFile }
    let standardized = url.standardizedFileURL
    let resolved = standardized.resolvingSymlinksInPath().standardizedFileURL

    if let allowedRoot {
      let root = allowedRoot.standardizedFileURL.resolvingSymlinksInPath().standardizedFileURL
      guard isContained(resolved, in: root) else { throw AudioError.outsideApprovedPath }
    } else {
      guard resolved.path == standardized.path else { throw AudioError.outsideApprovedPath }
    }

    return resolved
  }

  private func isContained(_ url: URL, in root: URL) -> Bool {
    url.path == root.path || url.path.hasPrefix(root.path + "/")
  }

  private func sha256(of url: URL) throws -> String {
    do {
      let handle = try FileHandle(forReadingFrom: url)
      defer { try? handle.close() }
      var hasher = SHA256()
      while let data = try handle.read(upToCount: 1024 * 1024), !data.isEmpty {
        hasher.update(data: data)
      }
      return hasher.finalize().map { String(format: "%02x", $0) }.joined()
    } catch {
      throw AudioError.operationFailed
    }
  }
}
