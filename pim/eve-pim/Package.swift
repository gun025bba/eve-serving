// swift-tools-version:5.9
import PackageDescription

// eve-pim: the Jarvis M1 EventKit helper (issue #68). macOS only.
// EvePimCore has no EventKit or socket code and is unit-tested with fakes;
// the eve-pim executable adds EventKit and the unix socket server.
let package = Package(
    name: "eve-pim",
    platforms: [.macOS(.v13)],
    products: [
        .executable(name: "eve-pim", targets: ["eve-pim"]),
    ],
    targets: [
        .target(name: "EvePimCore"),
        .executableTarget(name: "eve-pim", dependencies: ["EvePimCore"]),
        .testTarget(name: "EvePimCoreTests", dependencies: ["EvePimCore"]),
    ]
)
