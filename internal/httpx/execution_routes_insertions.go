package httpx

import (
	"context"
	"github.com/uvwt/agentdock/internal/app"
	"net/http"
)

// Resource dispatch shares the original authentication, deadline and JSON boundary.
func (h *activityHTTP) serveExecutionInsertions(ctx context.Context, w http.ResponseWriter, r *http.Request, runtime executionRuntime, parts []string) {
	finish, require := executionResponse(w, r)
	if len(parts) >= 3 && parts[0] == "conversations" && parts[2] == "insertions" {
		service, ok := h.runtime.(interface {
			RuntimeInsertions(context.Context, string) (app.Result, error)
			RuntimeEnqueueInsertion(context.Context, string, app.InsertionRequest) (app.Result, error)
			RuntimeCancelInsertion(context.Context, string, string) (app.Result, error)
		})
		if !ok {
			writeRuntimeAPIError(w, 503, "INSERTION_UNAVAILABLE", "insertion queue unavailable")
			return
		}
		if len(parts) == 3 && r.Method == http.MethodGet {
			result, err := service.RuntimeInsertions(ctx, parts[1])
			finish(result, err)
			return
		}
		if len(parts) == 3 && require("POST") {
			var input app.InsertionRequest
			if !decodeExecutionBody(w, r, &input) {
				return
			}
			result, err := service.RuntimeEnqueueInsertion(ctx, parts[1], input)
			finish(result, err)
			return
		}
		if len(parts) == 5 && parts[4] == "cancel" && require("POST") {
			var input struct{}
			if !decodeExecutionBody(w, r, &input) {
				return
			}
			result, err := service.RuntimeCancelInsertion(ctx, parts[1], parts[3])
			finish(result, err)
			return
		}
		return
	}
	writeRuntimeAPIError(w, 404, "NOT_FOUND", "execution route not found")
}
