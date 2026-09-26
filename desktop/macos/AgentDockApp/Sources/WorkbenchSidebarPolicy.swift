import Foundation

/// Presentation over server-ordered summaries; complete history remains paged.
enum WorkbenchSidebarPolicy {
    static func rows(_ group: WorkbenchWorkspaceGroup, expanded: Bool,
                     selected: String, now: Date?, fullHistory: Bool) -> [WorkbenchConversation] {
        if fullHistory { return group.conversations }
        let limit = expanded ? 15 : 5
        var ordinary = 0
        return group.conversations.filter { row in
            if row.pinned || row.inFlight || row.navigationID == selected || row.unattributed { return true }
            guard let now, let activity = row.lastActivityAt, activity <= now,
                  now.timeIntervalSince(activity) <= 3 * 86400 else { return false }
            ordinary += 1
            return ordinary <= limit
        }
    }
}
