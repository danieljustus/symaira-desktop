import Foundation

public enum AudioError: Error, Equatable, LocalizedError, Sendable {
  case notLocalFile
  case outsideApprovedPath
  case missingFile
  case directoryNotAllowed
  case unsupportedContainer(String)
  case missingAudioTrack
  case protectedMedia
  case unsupportedCodec
  case zeroLength
  case exceedsByteLimit
  case exceedsDurationLimit
  case invalidAudioFormat
  case destinationExists
  case cancelled
  case operationFailed

  public var errorDescription: String? {
    switch self {
    case .notLocalFile: "Only local file URLs are supported."
    case .outsideApprovedPath: "The media file is outside the approved local path."
    case .missingFile: "The media file does not exist."
    case .directoryNotAllowed: "A directory cannot be imported as media."
    case .unsupportedContainer(let container):
      "The media container is not supported: \(container)."
    case .missingAudioTrack: "The media file does not contain a usable audio track."
    case .protectedMedia: "Protected or DRM media cannot be processed locally."
    case .unsupportedCodec: "The audio codec is not available for local processing."
    case .zeroLength: "The media file has no duration."
    case .exceedsByteLimit: "The media file exceeds the configured size limit."
    case .exceedsDurationLimit: "The media file exceeds the configured duration limit."
    case .invalidAudioFormat: "The audio track cannot be converted to 16 kHz mono samples."
    case .destinationExists: "The original asset has already been imported."
    case .cancelled: "Audio import was cancelled."
    case .operationFailed: "The local media operation failed."
    }
  }
}
