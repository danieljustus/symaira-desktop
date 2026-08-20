import Foundation

public struct ModelCatalog: Sendable {
  public let descriptors: [ModelDescriptor]

  public init(descriptors: [ModelDescriptor]) {
    self.descriptors = descriptors
  }

  public static let beta = ModelCatalog(descriptors: [
    ModelDescriptor(
      id: "tiny",
      engineID: "whisperkit",
      displayName: "WhisperKit Tiny",
      source: "https://huggingface.co/argmaxinc/whisperkit-coreml",
      license: "MIT",
      expectedSizeBytes: 75_000_000,
      upstreamRevision: "openai_whisper-tiny",
      supportedArchitectures: ["arm64"]),
    ModelDescriptor(
      id: "large-v3-v20240930_626MB",
      engineID: "whisperkit",
      displayName: "WhisperKit Large v3",
      source: "https://huggingface.co/argmaxinc/whisperkit-coreml",
      license: "MIT",
      expectedSizeBytes: 626_000_000,
      upstreamRevision: "openai_whisper-large-v3-v20240930",
      supportedArchitectures: ["arm64"]),
  ])

  public func descriptor(for id: String) -> ModelDescriptor? {
    descriptors.first { $0.id == id }
  }
}
