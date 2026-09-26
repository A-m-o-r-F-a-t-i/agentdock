// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "AgentDockWorkbench",
    platforms: [.macOS(.v13)],
    products: [.library(name: "WorkbenchKit", targets: ["WorkbenchKit"])],
    targets: [
        .target(name: "WorkbenchKit", path: "Sources", exclude: ["main.swift"]),
        .testTarget(name: "WorkbenchRegressionTests", dependencies: ["WorkbenchKit"], path: "RegressionTests")
    ],
    swiftLanguageVersions: [.v5]
)
