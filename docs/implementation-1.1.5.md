# AgentDock 1.1.5 implementation map

Source: 878ef11e84b58c8c03119f3f9c7bbdc0a9a1edb3. Branch: feat/1.1.5-update.
Specification: plan-1.1.5.md, execution amendment takes precedence.

1. Sidebar projection: per-workspace paging, stable IDs, 72h/5/15/all, 60s reorder with 60s hysteresis.
2. Presentation: 120s genuine activity, 180s request-based insertion eligibility, shared theme resources and persisted notice dismissal.
3. Insertions: authenticated local management, immutable conversation owner, next new external root admission, absolute 300s expiry, bounded durable queue, append-only MCP content.
4. Managed MCP: CAS runtime/persistent overlays, metadata versus transport identity, safe refresh, contextual revision notices.
5. Completion: durable task success identity, application-wide deduplicated notification, exact task navigation.
6. Delivery: isolated automated regression and GitHub Windows build. No real-machine, installation, uninstall, upgrade, rollback or production-instance test in this delivery.

No runtime implementation or test is claimed complete by this document alone.

## Implemented responsibility map

| Responsibility | Production entry points | Automated coverage |
| --- | --- | --- |
| Independent activity windows | activity/measurements.go, activity/calls.go, ConversationActivityPolicy.cs | activity/execution_114_test.go; headless desktop policy assertions |
| Stable project projection | app/conversation_sidebar.go; ExecutionWindow.Sidebar.cs; SidebarOrdering.cs | conversation_sidebar_test.go; headless ordering assertions |
| Insertion state and transport | insertion/store.go; app/conversation_insertion.go; mcp/server.go | insertion/store_test.go; conversation_insertion_test.go |
| Managed overlays and generations | mcp/client/overrides.go, catalog_state.go, manager.go | overrides_test.go; existing client regression |
| Completion notification identity | taskstate/notifications.go; CompletionNotificationService.cs | notifications_test.go; markup/source contracts |
| One-time notice and live theme | ExecutionWindow.xaml.cs; MainWindow.Capabilities.cs; ThemeControls.cs | source contracts and desktop compilation; manual rendering not run |
| Delivery | windows-package.yml; test-install-windows.ps1 -StaticOnly | Go workflow contracts; verification-scope.json |

Completion of implementation does not imply an installation or manual UI test.
The final GitHub run and artifact manifest are the authoritative package records.
