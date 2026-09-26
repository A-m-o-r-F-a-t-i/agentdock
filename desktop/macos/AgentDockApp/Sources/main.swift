import AppKit
import Darwin
import Foundation

if CommandLine.arguments.contains("--unregister-background-services") {
    var failures: [String] = []
    MainActor.assumeIsolated {
        do {
            try ServiceController().unregisterManagedBackgroundServicesForUninstall()
        } catch {
            failures.append(error.localizedDescription)
        }
        do {
            try MenuLoginAgentController().unregisterForUninstall()
        } catch {
            failures.append(error.localizedDescription)
        }
    }
    if failures.isEmpty {
        exit(0)
    }
    let message = failures.joined(separator: "\n") + "\n"
    FileHandle.standardError.write(Data(message.utf8))
    exit(1)
}

if let screenshotArgument = CommandLine.arguments.first(where: { $0.hasPrefix("--render-workbench-screenshot=") }) {
    let output = String(screenshotArgument.dropFirst("--render-workbench-screenshot=".count))
    let requestedTheme = CommandLine.arguments
        .first(where: { $0.hasPrefix("--workbench-theme=") })
        .map { String($0.dropFirst("--workbench-theme=".count)) }
    var failure: String?
    MainActor.assumeIsolated {
        do {
            guard !output.isEmpty else {
                throw WorkbenchScreenshotError.encodingFailed
            }
            let application = NSApplication.shared
            application.setActivationPolicy(.accessory)
            L10n.setLanguagePreference(.simplifiedChinese)
            let theme = WorkbenchTheme(rawValue: requestedTheme ?? "light") ?? .light
            WorkbenchAppearance.shared.setTheme(theme, persist: false)
            let controller = WorkbenchWindowController(fixtureMode: true)
            controller.present()
            RunLoop.current.run(until: Date().addingTimeInterval(0.25))
            let url = URL(fileURLWithPath: output)
            try FileManager.default.createDirectory(
                at: url.deletingLastPathComponent(),
                withIntermediateDirectories: true
            )
            try controller.capturePNG(to: url)
            controller.close()
        } catch {
            failure = error.localizedDescription
        }
    }
    if let failure {
        FileHandle.standardError.write(Data((failure + "\n").utf8))
        exit(1)
    }
    exit(0)
}

MainActor.assumeIsolated {
    let application = NSApplication.shared
    let delegate = AppDelegate()
    ApplicationMenu.install(target: delegate)
    application.delegate = delegate
    application.setActivationPolicy(.accessory)
    application.run()
}
