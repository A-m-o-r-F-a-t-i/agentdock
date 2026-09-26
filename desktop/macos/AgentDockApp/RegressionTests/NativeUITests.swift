import XCTest
import AppKit
@testable import WorkbenchKit

final class NativeUITests: XCTestCase {
    private var previousLanguage: UILanguagePreference = .system
    override func setUp() {
        super.setUp()
        previousLanguage = L10n.languagePreference()
        L10n.setLanguagePreference(.simplifiedChinese)
    }
    override func tearDown() {
        L10n.setLanguagePreference(previousLanguage)
        super.tearDown()
    }

    @MainActor private func descendants(_ view: NSView) -> [NSView] {
        [view] + view.subviews.flatMap { descendants($0) }
    }
    @MainActor private func outputDirectory() throws -> URL {
        let path = ProcessInfo.processInfo.environment["WB06_EVIDENCE_DIR"] ?? NSTemporaryDirectory()
        let url = URL(fileURLWithPath: path)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }
    @MainActor private func capture(_ window: NSWindow, name: String) throws {
        let view = try XCTUnwrap(window.contentView)
        view.layoutSubtreeIfNeeded(); window.displayIfNeeded()
        let rep = try XCTUnwrap(view.bitmapImageRepForCachingDisplay(in: view.bounds))
        view.cacheDisplay(in: view.bounds, to: rep)
        let png = try XCTUnwrap(rep.representation(using: .png, properties: [:]))
        XCTAssertGreaterThan(png.count, 5000)
        try png.write(to: outputDirectory().appendingPathComponent(name))
    }
    @MainActor func testNativeWindowReuseThemesAndAccessibleControls() async throws {
        _ = NSApplication.shared
        NSApp.setActivationPolicy(.accessory)
        let controller = WorkbenchWindowController(fixtureMode: true)
        controller.present()
        let original = try XCTUnwrap(controller.window)
        controller.present()
        XCTAssertTrue(controller.window === original)
        XCTAssertEqual(original.title, "AgentDock Workbench")
        let all = descendants(try XCTUnwrap(original.contentView))
        XCTAssertNotNil(all.first { $0.accessibilityIdentifier() == "workbench.search.conversations" })
        XCTAssertNotNil(all.first { $0.accessibilityIdentifier() == "workbench.timeline" })
        let send = try XCTUnwrap(all.first { $0.accessibilityIdentifier() == "workbench.insertion.send" } as? NSButton)
        XCTAssertEqual(send.keyEquivalentModifierMask, [.command])
        let output = try XCTUnwrap(all.first { $0.accessibilityIdentifier() == "workbench.detail.response" } as? NSTextView)
        XCTAssertTrue(output.string.contains("尚未展开"))
        for theme in [WorkbenchTheme.light, .dark] {
            WorkbenchAppearance.shared.setTheme(theme, persist: false)
            original.contentView?.layoutSubtreeIfNeeded()
            try capture(original, name: "xctest-native-\(theme.rawValue).png")
        }
        original.setContentSize(NSSize(width: 1100, height: 730))
        try capture(original, name: "xctest-native-narrow.png")
        controller.close()
        XCTAssertFalse(original.isVisible)
        controller.present()
        XCTAssertTrue(controller.window === original)
        controller.close()
    }
    @MainActor func testCompletionPanelsAreBoundedAndDeduplicated() async throws {
        _ = NSApplication.shared
        let initial = NSApp.windows.filter { $0.isVisible && $0.title == L10n.text("Completed") }.count
        let observer = WorkbenchCompletionNotifications { _ in }
        for index in 0..<8 {
            let item = try XCTUnwrap(WorkbenchCompletion(json: .object([
                "id": .string("notification_\(index)"), "task_id": .string("task_\(index)"),
                "title": .string("Completed fixture \(index)")])))
            observer.show(item); observer.show(item)
        }
        let visible = NSApp.windows.filter { $0.isVisible && $0.title == L10n.text("Completed") }
        XCTAssertEqual(visible.count - initial, 3)
        if let window = visible.first { try capture(window, name: "xctest-task-completion.png") }
        observer.stop()
        XCTAssertEqual(NSApp.windows.filter { $0.isVisible && $0.title == L10n.text("Completed") }.count, initial)
    }
    @MainActor func testNativeResourceNavigationAndScreenshots() async throws {
        _ = NSApplication.shared
        RuntimeProtocol.configure { request in
            let path = request.url!.path
            let key = path.hasSuffix("mcp") ? "servers" : path.components(separatedBy: "/").last!
            var json: WorkbenchJSON = .object([key: .array([]), "total": .integer(0)])
            if path.hasSuffix("display") {
                json = .object(["revision": .integer(1), "chatgpt_mcp_ui_enabled": .bool(false),
                    "tool_output": .object(["enabled": .bool(true), "max_chars": .integer(20000)])])
            }
            if path.hasSuffix("effective") {
                json = .object(["effective": .object(["revision": .integer(1), "mode": .string("full"),
                    "custom_permissions_enabled": .bool(false), "settings_source": .string("execution_mode"),
                    "settings": .object(["permission_profile": .object(["filesystem": .string("write"), "network": .string("allow"), "sandbox_boundary": .string("none")]),
                        "approval_policy": .object(["mode": .string("on-request")]), "approval_reviewer": .string("user")])])])
            }
            return (200, "application/json", try json.encodedData(), 0)
        }
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [RuntimeProtocol.self]
        let api = WorkbenchAPIClient(session: URLSession(configuration: config)) {
            try WorkbenchConnection(baseURL: URL(string: "http://127.0.0.1:18765")!, bearerToken: "ui-fixture")
        }
        let manager = WorkbenchManagementWindow(client: api)
        for resource in WorkbenchResource.allCases {
            manager.present(resource)
            try await Task.sleep(nanoseconds: 100_000_000)
            XCTAssertTrue(manager.window!.isVisible)
            try capture(manager.window!, name: "xctest-manager-\(resource.rawValue).png")
        }
        manager.close()
        let editor = WorkbenchPermissionEditor(client: api)
        editor.present(workspaceID: "wsp_fixture")
        try await Task.sleep(nanoseconds: 100_000_000)
        let check = try XCTUnwrap(descendants(editor.window!.contentView!).first {
            $0.accessibilityIdentifier() == "workbench.permission.custom"
        } as? NSButton)
        XCTAssertEqual(check.state, .off)
        XCTAssertTrue(check.isEnabled)
        try capture(editor.window!, name: "xctest-permission-disabled.png")
        check.performClick(nil)
        XCTAssertEqual(check.state, .on)
        try capture(editor.window!, name: "xctest-permission-enabled.png")
        editor.close()
        XCTAssertFalse(RuntimeProtocol.captured().contains { $0.httpMethod == "POST" })
    }
}
