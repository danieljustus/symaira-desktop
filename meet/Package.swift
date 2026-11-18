// swift-tools-version: 6.1
import PackageDescription

let package = Package(
  name: "symaira-meet",
  platforms: [
    .macOS(.v15),
    .iOS(.v18),
  ],
  products: [
    .library(name: "SymMeetCore", targets: ["SymMeetCore"]),
    .library(name: "SymDictationCore", targets: ["SymDictationCore"]),
    .library(name: "SymMeetCapture", targets: ["SymMeetCapture"]),
    .library(name: "SymMeetMCP", targets: ["SymMeetMCP"]),
    .library(name: "SymMeetWhisperKit", targets: ["SymMeetWhisperKit"]),
    .library(name: "SymMeetSpeakerKit", targets: ["SymMeetSpeakerKit"]),
    .library(name: "SymMeetParakeet", targets: ["SymMeetParakeet"]),
    .executable(name: "symmeet", targets: ["symmeet"]),
  ],
  dependencies: [
    .package(url: "https://github.com/apple/swift-argument-parser.git", exact: "1.8.2"),
    .package(url: "https://github.com/argmaxinc/argmax-oss-swift", exact: "1.1.1"),
    .package(url: "https://github.com/danieljustus/symaira-corekit.git", exact: "0.23.0"),
    // Pinned to 0.18.3 (revision c8b41f98eae668794dd0083bbe028fe7e6585051); Apache-2.0.
    .package(url: "https://github.com/FluidInference/FluidAudio.git", exact: "0.18.3"),
  ],
  targets: [
    .target(name: "SymMeetCore"),
    .target(name: "SymDictationCore"),
    .target(
      name: "SymMeetCapture",
      dependencies: ["SymMeetCore"],
      swiftSettings: [
        .enableUpcomingFeature("ExistentialAny"),
      ]
    ),
    .target(
      name: "SymMeetMCP",
      dependencies: [
        "SymMeetCore",
        .product(name: "SymairaMCP", package: "symaira-corekit"),
      ]
    ),
    .target(
      name: "SymMeetSpeakerKit",
      dependencies: [
        "SymMeetCore",
        .product(name: "SpeakerKit", package: "argmax-oss-swift"),
      ],
      resources: [.copy("THIRD_PARTY_NOTICES.md")]
    ),
    .target(
      name: "SymMeetWhisperKit",
      dependencies: [
        "SymMeetCore",
        .product(name: "WhisperKit", package: "argmax-oss-swift"),
      ],
      resources: [.copy("THIRD_PARTY_NOTICES.md")]
    ),
    .target(
      name: "SymMeetParakeet",
      dependencies: [
        "SymMeetCore",
        .product(name: "FluidAudio", package: "FluidAudio"),
      ],
      resources: [.copy("THIRD_PARTY_NOTICES.md")]
    ),
    .executableTarget(
      name: "symmeet",
      dependencies: [
        "SymMeetCore",
        "SymMeetCapture",
        "SymMeetMCP",
        "SymMeetWhisperKit",
        "SymMeetSpeakerKit",
        .product(name: "ArgumentParser", package: "swift-argument-parser"),
        .product(name: "SymairaUpdateCheck", package: "symaira-corekit"),
      ],
      exclude: ["Info.plist"],
      linkerSettings: [
        .unsafeFlags([
          "-Xlinker", "-sectcreate",
          "-Xlinker", "__TEXT",
          "-Xlinker", "__info_plist",
          "-Xlinker", "Sources/symmeet/Info.plist",
        ]),
      ]
    ),
    .testTarget(
      name: "SymMeetCoreTests",
      dependencies: ["SymMeetCore"],
      path: "Tests",
      exclude: [
        "SymMeetCLITests", "SymMeetMCPTests", "SymMeetWhisperKitTests",
        "SymMeetCaptureTests", "SymMeetSpeakerKitTests", "SymDictationCoreTests",
        "SymMeetParakeetTests",
      ],
      sources: ["Support/FakeTranscriptionEngine.swift", "SymMeetCoreTests"],
      resources: [.copy("Fixtures/contracts"), .copy("Fixtures/exports"), .copy("Fixtures/integration")]
    ),
    .testTarget(
      name: "SymDictationCoreTests",
      dependencies: ["SymDictationCore"],
      path: "Tests/SymDictationCoreTests"
    ),
    .testTarget(
      name: "SymMeetParakeetTests",
      dependencies: ["SymMeetParakeet", "SymMeetCore"],
      path: "Tests/SymMeetParakeetTests"
    ),
    .testTarget(
      name: "SymMeetMCPTests",
      dependencies: [
        "SymMeetMCP",
        .product(name: "SymairaMCP", package: "symaira-corekit"),
      ]
    ),
    .testTarget(name: "SymMeetCLITests", dependencies: ["SymMeetCore"]),
    .testTarget(name: "SymMeetWhisperKitTests", dependencies: ["SymMeetWhisperKit"]),
    .testTarget(
      name: "SymMeetSpeakerKitTests",
      dependencies: ["SymMeetSpeakerKit", "SymMeetCore"],
      path: "Tests/SymMeetSpeakerKitTests"
    ),
    .testTarget(
      name: "SymMeetCaptureTests",
      dependencies: ["SymMeetCapture"],
      path: "Tests/SymMeetCaptureTests"
    ),
  ],
  swiftLanguageModes: [.v6]
)
