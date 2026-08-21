// swift-tools-version:6.0
import PackageDescription

let package = Package(
    name: "SymroomClient",
    platforms: [
        .macOS(.v14),
    ],
    products: [
        .library(name: "SymroomKit", targets: ["SymroomKit"]),
        .library(name: "SymroomFeature", targets: ["SymroomFeature"]),
    ],
    dependencies: [
        .package(url: "https://github.com/danieljustus/symaira-appkit.git", exact: "0.7.0"),
    ],
    targets: [
        // CLI bridge + models — reads symroom's --json output, never
        // reimplements room logic.
        .target(
            name: "SymroomKit",
            dependencies: [
                .product(name: "SymairaCLIRunner", package: "symaira-appkit"),
                .product(name: "SymairaToolKit", package: "symaira-appkit"),
            ]
        ),
        // Feature module (views + state, no app entry) — consumed by the
        // Symaira Hub. The standalone GUI app is a separate decision (Gate G2).
        .target(
            name: "SymroomFeature",
            dependencies: [
                "SymroomKit",
                .product(name: "SymairaTheme", package: "symaira-appkit"),
            ]
        ),
        .testTarget(
            name: "SymroomFeatureTests",
            dependencies: ["SymroomKit"]
        ),
    ]
)
