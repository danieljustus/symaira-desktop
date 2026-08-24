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
        .package(url: "https://github.com/danieljustus/symaira-appkit.git", exact: "0.10.0"),
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
        // Feature module (views + state, no app entry) — embedded by SymDesk
        // as its Project Journal surface (issue #517). symroom is the one
        // absorbed tool without an in-process call site, so this stays a CLI
        // bridge and degrades to an install tile when the binary is absent.
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
