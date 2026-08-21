// swift-tools-version:6.0
import PackageDescription

// Root package: lets consumers (Symaira Hub) pin this repository by tag.
// SPM cannot pin a package that lives in a subdirectory (client/), so this
// mirror re-exposes the client package's library target from the repo root.
// KEEP IN SYNC with client/Package.swift — that manifest is the source of
// truth for target definitions and dependencies.
let package = Package(
    name: "SymseekClient",
    platforms: [
        .macOS(.v14)
    ],
    products: [
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
            path: "client/Sources/SymseekFeature"
        ),
    ]
)
