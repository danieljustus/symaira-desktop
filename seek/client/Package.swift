// swift-tools-version:6.0
import PackageDescription

let package = Package(
    name: "SymseekClient",
    platforms: [
        .macOS(.v14)
    ],
    products: [
        .executable(name: "symseek-gui", targets: ["SymseekApp"]),
        .library(name: "SymseekFeature", targets: ["SymseekFeature"]),
    ],
    dependencies: [
        .package(url: "https://github.com/danieljustus/symaira-appkit.git", exact: "0.7.0"),
    ],
    targets: [
        // Feature module (views + engine supervision, no app entry) —
        // consumed by the thin standalone app and the Symaira Hub.
        .target(
            name: "SymseekFeature",
            dependencies: [
                .product(name: "SymairaTheme", package: "symaira-appkit"),
                .product(name: "SymairaToolKit", package: "symaira-appkit"),
                .product(name: "SymairaDaemonKit", package: "symaira-appkit"),
            ],
            path: "Sources/SymseekFeature"
        ),
        .executableTarget(
            name: "SymseekApp",
            dependencies: ["SymseekFeature"],
            path: "Sources/SymseekApp",
            resources: []
        ),
        .testTarget(
            name: "SymseekFeatureTests",
            dependencies: ["SymseekFeature"],
            path: "Tests/SymseekFeatureTests"
        )
    ]
)
