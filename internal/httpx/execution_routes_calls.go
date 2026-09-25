package httpx

import (
	"context"
	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/app"
	"net/http"
)

// Resource dispatch shares the original authentication, deadline and JSON boundary.
func (h *activityHTTP) serveExecutionCalls(ctx context.Context, w http.ResponseWriter, r *http.Request, runtime executionRuntime, parts []string) {
	finish, require := executionResponse(w, r)
	if parts[0] == "calls" {
		if len(parts) == 4 && parts[2] == "payload" {
			if !require("GET") {
				return
			}
			page, err := readExecutionPayload(ctx, h.runtime.ActivityJournal(), parts[1], parts[3], r.URL.Query())
			finish(page, err)
			return
		}
		if len(parts) == 2 && parts[1] == "batch" {
			if !require("POST") {
				return
			}
			var request app.BatchRequest
			if !decodeExecutionBody(w, r, &request) {
				return
			}
			manager, ok := h.runtime.(interface {
				RuntimeCallManagementBatch(context.Context, app.BatchRequest) (app.BatchResult, error)
			})
			if !ok {
				writeRuntimeAPIError(w, 503, "MANAGEMENT_UNAVAILABLE", "call management is unavailable")
				return
			}
			result, err := manager.RuntimeCallManagementBatch(ctx, request)
			finish(result, err)
			return
		}
		if len(parts) == 1 || len(parts) == 2 && (parts[1] == "stream" || parts[1] == "export") {
			if !require("GET") {
				return
			}
			query, err := callQuery(r)
			if err != nil {
				executionError(w, err)
				return
			}
			if len(parts) == 2 && parts[1] == "stream" {
				h.streamCalls(w, r, query)
				return
			}
			if len(parts) == 2 && parts[1] == "export" {
				query.IncludeOutput = true
				w.Header().Set("Content-Disposition", `attachment; filename="agentdock-execution.json"`)
			}
			page, err := h.runtime.ActivityJournal().Calls(ctx, query)
			finish(page, err)
			return
		}
		if len(parts) == 2 {
			if !require("GET") {
				return
			}
			call, err := h.runtime.ActivityJournal().Call(ctx, parts[1])
			finish(call, err)
			return
		}
		if len(parts) == 3 && parts[2] == "stop" {
			if !require("POST") {
				return
			}
			var body struct{}
			if !decodeExecutionBody(w, r, &body) {
				return
			}
			result, err := runtime.RuntimeCallStop(ctx, parts[1])
			finish(result, err)
			return
		}
		if len(parts) == 3 && parts[2] == "events" {
			if !require("GET") {
				return
			}
			query, err := callQuery(r)
			if err != nil {
				executionError(w, err)
				return
			}
			page, err := h.runtime.ActivityJournal().Query(ctx, activity.Query{CallID: parts[1], After: query.After, Limit: query.Limit})
			finish(page, err)
			return
		}
	}
	writeRuntimeAPIError(w, 404, "NOT_FOUND", "execution route not found")
}
