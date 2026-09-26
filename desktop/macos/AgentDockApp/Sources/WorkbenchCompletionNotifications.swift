import AppKit

struct WorkbenchCompletion: Equatable {
    let id: String
    let taskID: String
    let title: String
    let conversationID: String
    init?(json: WorkbenchJSON) {
        let id = json.text("id"), taskID = json.text("task_id")
        guard !id.isEmpty, !taskID.isEmpty else { return nil }
        self.id = id; self.taskID = taskID
        title = json.text("title"); conversationID = json.text("conversation_id")
    }
}

/// App-owned observer, independent of Workbench window visibility. Core claims
/// completion identities; the client neither mints them nor marks tasks complete.
@MainActor
final class WorkbenchCompletionNotifications {
    private let client: WorkbenchAPIClient
    private let navigate: (WorkbenchCompletion) -> Void
    private var polling: Task<Void, Never>?
    private var panels = [String: WorkbenchCompletionPanel]()
    private var recentlyShown = [String]()
    init(client: WorkbenchAPIClient = WorkbenchAPIClient(), navigate: @escaping (WorkbenchCompletion) -> Void) {
        self.client = client; self.navigate = navigate
    }
    deinit { polling?.cancel() }
    func start() {
        guard polling == nil else { return }
        polling = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                var seconds: UInt64 = 3
                if panels.count < 3 {
                    do {
                        let result = try await client.post("/internal/runtime/execution/notifications",
                            body: .object(["limit": .integer(Int64(3 - panels.count))]))
                        try Task.checkCancellation()
                        for raw in result.values("notifications").prefix(3 - panels.count) {
                            guard let item = WorkbenchCompletion(json: raw), !recentlyShown.contains(item.id) else { continue }
                            show(item)
                        }
                    } catch {
                        if Task.isCancelled { return }
                        seconds = 15
                    }
                }
                do { try await Task.sleep(nanoseconds: seconds * 1_000_000_000) } catch { return }
            }
        }
    }
    func stop() {
        polling?.cancel(); polling = nil
        for panel in Array(panels.values) { panel.close() }
        panels.removeAll()
    }
    func show(_ item: WorkbenchCompletion) {
        guard panels.count < 3, panels[item.id] == nil, !recentlyShown.contains(item.id) else { return }
        recentlyShown.append(item.id)
        if recentlyShown.count > 128 { recentlyShown.removeFirst(recentlyShown.count - 128) }
        let panel = WorkbenchCompletionPanel(item: item, open: { [weak self] in self?.navigate(item) })
        panel.onClose = { [weak self] in self?.panels.removeValue(forKey: item.id); self?.reposition() }
        panels[item.id] = panel
        reposition(); panel.showWindow(nil)
    }
    private func reposition() {
        guard let screen = NSScreen.main else { return }
        let area = screen.visibleFrame
        for (index, key) in panels.keys.sorted().enumerated() {
            guard let window = panels[key]?.window else { continue }
            window.setFrameOrigin(NSPoint(x: area.maxX - window.frame.width - 16,
                y: area.maxY - CGFloat(index + 1) * (window.frame.height + 12)))
        }
    }
}

@MainActor
final class WorkbenchCompletionPanel: NSWindowController, NSWindowDelegate {
    var onClose: (() -> Void)?
    private let open: () -> Void
    private var timeout: Task<Void, Never>?
    init(item: WorkbenchCompletion, open: @escaping () -> Void) {
        self.open = open
        let panel = NSPanel(contentRect: NSRect(x: 0, y: 0, width: 390, height: 140),
            styleMask: [.titled, .closable, .nonactivatingPanel], backing: .buffered, defer: false)
        panel.title = L10n.text("Task completed")
        panel.isReleasedWhenClosed = false; panel.hidesOnDeactivate = false
        panel.level = .floating
        super.init(window: panel); panel.delegate = self
        let stack = WorkbenchUI.stack(.vertical, spacing: 10)
        stack.addArrangedSubview(WorkbenchUI.label(item.title, font: .systemFont(ofSize: 14, weight: .semibold), lines: 2))
        stack.addArrangedSubview(WorkbenchUI.label(item.taskID, font: .systemFont(ofSize: 11)))
        stack.addArrangedSubview(WorkbenchUI.button(L10n.text("Open task"), target: self, action: #selector(openTask)))
        panel.contentView?.addSubview(stack)
        stack.pinEdges(to: panel.contentView!, insets: NSEdgeInsets(top: 12, left: 16, bottom: 12, right: 16))
        timeout = Task { [weak self] in
            do { try await Task.sleep(nanoseconds: 15_000_000_000) } catch { return }
            self?.close()
        }
    }
    required init?(coder: NSCoder) { nil }
    deinit { timeout?.cancel() }
    @objc private func openTask() { open(); close() }
    func windowWillClose(_ notification: Notification) { timeout?.cancel(); onClose?() }
}
