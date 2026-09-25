package httpx

import (
	"context"
	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/permission"
	"net/http"
)

// Resource dispatch shares the original authentication, deadline and JSON boundary.
func (h *activityHTTP) serveExecutionPermissions(ctx context.Context, w http.ResponseWriter, r *http.Request, runtime executionRuntime, parts []string) {
	finish, require := executionResponse(w, r)
	if parts[0] == "permissions" {
		if len(parts) == 2 && parts[1] == "effective" {
			if !require("GET") {
				return
			}
			binding := activity.Binding{ConversationID: r.URL.Query().Get("conversation_id"), WorkspaceID: r.URL.Query().Get("workspace_id")}
			if err := binding.Validate(); err != nil {
				executionError(w, err)
				return
			}
			result, err := runtime.RuntimePermissions(ctx, binding)
			finish(result, err)
			return
		}
		if len(parts) == 1 {
			if !require("POST") {
				return
			}
			var change permission.Change
			if !decodeExecutionBody(w, r, &change) {
				return
			}
			result, err := runtime.RuntimePermissionsUpdate(ctx, change)
			finish(result, err)
			return
		}
	}
	writeRuntimeAPIError(w, 404, "NOT_FOUND", "execution route not found")
}
