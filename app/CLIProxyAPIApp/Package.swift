// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "CLIProxyAPIApp",
    platforms: [
        .macOS(.v14),
    ],
    products: [
        .executable(name: "CLIProxyAPIApp", targets: ["CLIProxyAPIApp"]),
    ],
    targets: [
        .executableTarget(
            name: "CLIProxyAPIApp",
            path: "Sources/CLIProxyAPIApp",
            swiftSettings: [
                .enableUpcomingFeature("StrictConcurrency"),
            ]
        ),
    ]
)
