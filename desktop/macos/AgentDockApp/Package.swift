// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "AgentDockWorkbench",
    defaultLocalization: "en",
    platforms: [.macOS(.v13)],
    products: [.library(name: "WorkbenchKit", targets: ["WorkbenchKit"])],
    targets: [
        .target(name: "WorkbenchKit", path: ".", exclude: ["Sources/main.swift", "Tests", "RegressionTests"],
                sources: ["Sources"], resources: [.process("Resources")]),
        .testTarget(name: "WorkbenchRegressionTests", dependencies: ["WorkbenchKit"], path: "RegressionTests")
    ],
    swiftLanguageVersions: [.v5]
)
