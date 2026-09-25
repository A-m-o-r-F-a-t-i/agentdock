package httpx

import (
	"context"
	"net/http"
)

// Resource dispatch shares the original authentication, deadline and JSON boundary.
func (h *activityHTTP) serveExecutionApprovals(ctx context.Context, w http.ResponseWriter, r *http.Request, runtime executionRuntime, parts []string) {
	finish, require := executionResponse(w, r)
	if parts[0] == "approvals" {
		if len(parts) == 1 {
			if !require("GET") {
				return
			}
			offset, limit, err := executionPaging(r)
			if err != nil {
				executionError(w, err)
				return
			}
			result, err := runtime.RuntimeApprovals(ctx, r.URL.Query().Get("status"), offset, limit)
			finish(result, err)
			return
		}
		if len(parts) == 2 {
			if !require("GET") {
				return
			}
			result, err := runtime.RuntimeApprovalRequest(ctx, parts[1])
			finish(result, err)
			return
		}
		if len(parts) == 3 && (parts[2] == "approve" || parts[2] == "reject") {
			if !require("POST") {
				return
			}
			var request struct {
				AllowWorkspace bool `json:"allow_workspace,omitempty"`
			}
			if !decodeExecutionBody(w, r, &request) {
				return
			}
			result, err := runtime.RuntimeApprovalDecision(ctx, parts[1], parts[2], request.AllowWorkspace)
			finish(result, err)
			return
		}
	}
	writeRuntimeAPIError(w, 404, "NOT_FOUND", "execution route not found")
}
