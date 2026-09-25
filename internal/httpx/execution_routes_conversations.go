package httpx

import (
	"context"
	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/app"
	"net/http"
	"strconv"
)

// Resource dispatch shares the original authentication, deadline and JSON boundary.
func (h *activityHTTP) serveExecutionConversations(ctx context.Context, w http.ResponseWriter, r *http.Request, runtime executionRuntime, parts []string) {
	finish, require := executionResponse(w, r)
	if len(parts) == 1 && parts[0] == "conversations" {
		if !require("GET") {
			return
		}
		offset, limit, err := executionPaging(r)
		if err != nil {
			executionError(w, err)
			return
		}
		q := r.URL.Query()
		selection := false
		if raw := q.Get("selection"); raw != "" {
			selection, err = strconv.ParseBool(raw)
			if err != nil {
				executionError(w, err)
				return
			}
		}
		page, err := runtime.RuntimeConversations(ctx, app.ExecutionListQuery{Selection: selection, View: q.Get("view"), Search: q.Get("search"), WorkspaceID: q.Get("workspace_id"), Tag: q.Get("tag"), Offset: offset, Limit: limit})
		finish(page, err)
		return
	}
	if len(parts) == 2 && (parts[0] == "tasks" || parts[0] == "conversations") && parts[1] == "batch" {
		if !require("POST") {
			return
		}
		var request app.BatchRequest
		if !decodeExecutionBody(w, r, &request) {
			return
		}
		kind := "task"
		if parts[0] == "conversations" {
			kind = "conversation"
		}
		result, err := runtime.RuntimeManagementBatch(ctx, kind, request)
		finish(result, err)
		return
	}
	if len(parts) >= 2 && parts[0] == "conversations" {
		id := parts[1]
		if len(parts) == 2 {
			if r.Method == "DELETE" {
				result, err := runtime.RuntimeManagementBatch(ctx, "conversation", app.BatchRequest{IDs: []string{id}, Action: "delete", ConfirmPermanent: r.URL.Query().Get("confirm") == "true"})
				finish(result, err)
				return
			}
			if !require("GET") {
				return
			}
			result, err := runtime.RuntimeConversation(ctx, id)
			finish(result, err)
			return
		}
		if len(parts) == 3 && (parts[2] == "terminate" || parts[2] == "resume") {
			if !require("POST") {
				return
			}
			var request app.ConversationLifecycleRequest
			if !decodeExecutionBody(w, r, &request) {
				return
			}
			controller, ok := h.runtime.(interface {
				RuntimeConversationLifecycle(context.Context, string, string, app.ConversationLifecycleRequest) (app.Result, error)
			})
			if !ok {
				writeRuntimeAPIError(w, 503, "LIFECYCLE_UNAVAILABLE", "conversation lifecycle is unavailable")
				return
			}
			result, err := controller.RuntimeConversationLifecycle(ctx, id, parts[2], request)
			finish(result, err)
			return
		}
		if len(parts) == 3 && parts[2] == "current-task" {
			if !require("POST") {
				return
			}
			var request app.ConversationBindingRequest
			if !decodeExecutionBody(w, r, &request) {
				return
			}
			binder, ok := h.runtime.(interface {
				RuntimeConversationBinding(context.Context, string, app.ConversationBindingRequest) (app.Result, error)
			})
			if !ok {
				writeRuntimeAPIError(w, 503, "BINDING_UNAVAILABLE", "conversation continuation is unavailable")
				return
			}
			result, err := binder.RuntimeConversationBinding(ctx, id, request)
			finish(result, err)
			return
		}
		if len(parts) == 3 && parts[2] == "link-task" {
			if !require("POST") {
				return
			}
			var request struct {
				TaskID string `json:"task_id"`
			}
			if !decodeExecutionBody(w, r, &request) {
				return
			}
			result, err := runtime.RuntimeLinkConversation(ctx, id, request.TaskID)
			finish(result, err)
			return
		}
		if len(parts) == 3 && (parts[2] == "calls" || parts[2] == "stream") {
			if !require("GET") {
				return
			}
			query, err := callQuery(r)
			if err != nil {
				executionError(w, err)
				return
			}
			query.ConversationID = id
			if parts[2] == "stream" {
				h.streamCalls(w, r, query)
				return
			}
			page, err := h.runtime.ActivityJournal().Calls(ctx, query)
			finish(page, err)
			return
		}
	}
	if len(parts) == 3 && (parts[0] == "tasks" || parts[0] == "conversations") {
		id, action := parts[1], parts[2]
		if action == "calls" {
			if !require("GET") {
				return
			}
			query, err := callQuery(r)
			if err != nil {
				executionError(w, err)
				return
			}
			query.TaskID = id
			page, err := h.runtime.ActivityJournal().Calls(ctx, query)
			finish(page, err)
			return
		}
		if action == "trash" || action == "restore" || action == "metadata" {
			if !require("POST") {
				return
			}
			var change activity.MetadataChange
			if !decodeExecutionBody(w, r, &change) {
				return
			}
			if action != "metadata" {
				change.Action = action
			}
			kind := "task"
			if parts[0] == "conversations" {
				kind = "conversation"
			}
			request := app.BatchRequest{IDs: []string{id}, Action: change.Action, Title: change.Title, Tags: change.Tags, RetentionDays: change.RetentionDays}
			result, err := runtime.RuntimeManagementBatch(ctx, kind, request)
			finish(result, err)
			return
		}
	}
	writeRuntimeAPIError(w, 404, "NOT_FOUND", "execution route not found")
}
