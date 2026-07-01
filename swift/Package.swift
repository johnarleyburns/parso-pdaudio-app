// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "ParsoKit",
    platforms: [
        .iOS(.v15),
        .macOS(.v12),
        .tvOS(.v15),
    ],
    products: [
        .library(name: "ParsoKit", targets: ["ParsoKit"]),
    ],
    targets: [
        .target(
            name: "ParsoKit",
            // SQLite3 is provided by the Apple SDKs; no third-party dependency.
            linkerSettings: [.linkedLibrary("sqlite3")]
        ),
        .testTarget(
            name: "ParsoKitTests",
            dependencies: ["ParsoKit"]
        ),
    ]
)
