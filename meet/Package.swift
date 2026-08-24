// swift-tools-version: 6.1
import PackageDescription

let package = Package(
  name: "symaira-meet",
  platforms: [
    .macOS(.v15)
  ],
  products: [
    .library(name: "SymMeetCore", targets: ["SymMeetCore"]),
    .library(name: "SymMeetCapture", targets: ["SymMeetCapture"]),
    .library(name: "SymMeetMCP", targets: ["SymMeetMCP"]),
    .library(name: "SymMeetWhisperKit", targets: ["SymMeetWhisperKit"]),
    .library(name: "SymMeetSpeakerKit", targets: ["SymMeetSpeakerKit"]),
    .executable(name: "symmeet", targets: ["symmeet"]),
  ],
  dependencies: [
    .package(url: "https://github.com/apple/swift-argument-parser.git", exact: "1.8.2"),
    .package(url: "https://github.com/argmaxinc/argmax-oss-swift", exact: "1.1.0"),
    .package(url: "https://github.com/danieljustus/symaira-appkit.git", exact: "0.10.0"),
  ],
  targets: [
    .target(name: "SymMeetCore"),
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
        .product(name: "SymairaMCP", package: "symaira-appkit"),
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
    .executableTarget(
      name: "symmeet",
      dependencies: [
        "SymMeetCore",
        "SymMeetCapture",
        "SymMeetMCP",
        "SymMeetWhisperKit",
        "SymMeetSpeakerKit",
        .product(name: "ArgumentParser", package: "swift-argument-parser"),
        .product(name: "SymairaUpdateCheck", package: "symaira-appkit"),
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
        "SymMeetCaptureTests", "SymMeetSpeakerKitTests",
      ],
      sources: ["Support/FakeTranscriptionEngine.swift", "SymMeetCoreTests"],
      resources: [.copy("Fixtures/contracts"), .copy("Fixtures/exports"), .copy("Fixtures/integration")]
    ),
    .testTarget(
      name: "SymMeetMCPTests",
      dependencies: [
        "SymMeetMCP",
        .product(name: "SymairaMCP", package: "symaira-appkit"),
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
