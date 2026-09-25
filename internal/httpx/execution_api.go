package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/permission"
	"github.com/uvwt/agentdock/internal/taskstate"
)

type executionRuntime interface {
	RuntimeExecutionOverview(context.Context) (app.Result, error)
	RuntimeConversations(context.Context, app.ExecutionListQuery) (app.ConversationPage, error)
	RuntimeConversation(context.Context, string) (app.Result, error)
	RuntimeLinkConversation(context.Context, string, string) (app.Result, error)
	RuntimeManagedTasks(context.Context, taskstate.TaskQuery) (taskstate.ManagedTaskPage, error)
	RuntimeManagementBatch(context.Context, string, app.BatchRequest) (app.BatchResult, error)
	RuntimePermissions(context.Context, activity.Binding) (app.Result, error)
	RuntimePermissionsUpdate(context.Context, permission.Change) (app.Result, error)
	RuntimeApprovals(context.Context, string, int, int) (app.Result, error)
	RuntimeApprovalRequest(context.Context, string) (app.Result, error)
	RuntimeApprovalDecision(context.Context, string, string, bool) (app.Result, error)
	RuntimeCallStop(context.Context, string) (app.Result, error)
}

func isExecutionRoute(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/internal/runtime/"), "/")
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "execution", "conversations", "calls", "approvals", "permissions":
		return true
	}
	if parts[0] == "tasks" {
		if len(parts) == 2 && parts[1] == "batch" {
			return true
		}
		if len(parts) == 3 {
			switch parts[2] {
			case "trash", "restore", "metadata", "calls":
				return true
			}
		}
	}
	return false
}
func executionError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	code := "INVALID_EXECUTION_REQUEST"
	if errors.Is(err, activity.ErrCallNotFound) || errors.Is(err, activity.ErrConversationNotFound) || errors.Is(err, permission.ErrApprovalNotFound) || errors.Is(err, taskstate.ErrTaskNotFound) {
		status = http.StatusNotFound
		code = "NOT_FOUND"
	}
	if errors.Is(err, permission.ErrRevision) || errors.Is(err, permission.ErrApprovalExpired) || errors.Is(err, activity.ErrConversationConflict) {
		status = http.StatusConflict
		code = "STALE_PERMISSION"
	}
	if errors.Is(err, activity.ErrConversationDeleted) {
		status = http.StatusGone
		code = "SOURCE_DELETED"
	}
	var toolErr *app.ToolError
	if errors.As(err, &toolErr) {
		writeRuntimeAPIHandlerError(w, err)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
		code = "EXECUTION_QUERY_TIMEOUT"
	}
	writeRuntimeAPIError(w, status, code, err.Error())
}
func decodeExecutionBody(w http.ResponseWriter, r *http.Request, value any) bool {
	content, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || content != "application/json" {
		writeRuntimeAPIError(w, 415, "JSON_REQUIRED", "application/json is required")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		writeRuntimeAPIError(w, 400, "INVALID_ARGUMENT", "invalid execution control request: "+err.Error())
		return false
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		writeRuntimeAPIError(w, 400, "INVALID_ARGUMENT", "exactly one JSON request is required")
		return false
	}
	return true
}
func executionPaging(r *http.Request) (offset, limit int, err error) {
	limit = 100
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 20000 {
			return 0, 0, errors.New("invalid offset")
		}
	}
	limit, err = activityLimit(r, 200)
	return
}
func callQuery(r *http.Request) (query activity.CallQuery, err error) {
	values := r.URL.Query()
	query = activity.CallQuery{View: values.Get("view"), ConversationID: values.Get("conversation_id"), TaskID: values.Get("task_id"), ThreadID: values.Get("thread_id"), ParentCallID: values.Get("parent_call_id"), Status: values.Get("status"), Search: values.Get("search")}
	query.Limit, err = activityLimit(r, 200)
	if err != nil {
		return
	}
	for key, target := range map[string]*uint64{"before": &query.Before, "after": &query.After} {
		if value := values.Get(key); value != "" {
			*target, err = strconv.ParseUint(value, 10, 64)
			if err != nil {
				return query, errors.New("invalid call cursor")
			}
		}
	}
	query.Updates = values.Has("after")
	for key, target := range map[string]*bool{"unattributed": &query.Unattributed, "top_level": &query.TopLevel, "include_output": &query.IncludeOutput, "include_diagnostic": &query.IncludeDiagnostic} {
		if value := values.Get(key); value != "" {
			*target, err = strconv.ParseBool(value)
			if err != nil {
				return query, errors.New("invalid boolean query")
			}
		}
	}
	if query.ConversationID != "" && query.Unattributed {
		return query, errors.New("unknown-source and conversation filters cannot be combined")
	}
	err = (activity.Binding{ConversationID: query.ConversationID, TaskID: query.TaskID, ThreadID: query.ThreadID, ParentCallID: query.ParentCallID}).Validate()
	return
}

// executionResponse owns response/error and method handling for each resource.
// Authentication and the request deadline remain at serveExecution's boundary.
func executionResponse(w http.ResponseWriter, r *http.Request) (func(any, error), func(string) bool) {
	finish := func(result any, err error) {
		if err != nil {
			executionError(w, err)
		} else {
			writeJSON(w, result)
		}
	}
	require := func(method string) bool {
		if r.Method != method {
			activityMethodError(w, method)
			return false
		}
		return true
	}
	return finish, require
}

// serveExecution is invoked only after the existing direct-loopback, origin,
// reverse-proxy and authenticated-client checks in activityHTTP.ServeHTTP.
func (h *activityHTTP) serveExecution(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.runtime.(executionRuntime)
	if !ok {
		writeRuntimeAPIError(w, 503, "EXECUTION_UNAVAILABLE", "execution center is unavailable")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/internal/runtime/"), "/")
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	switch parts[0] {
	case "execution":
		h.serveExecutionOverview(ctx, w, r, runtime, parts)
	case "conversations", "tasks":
		if len(parts) >= 3 && parts[0] == "conversations" && parts[2] == "insertions" {
			h.serveExecutionInsertions(ctx, w, r, runtime, parts)
		} else {
			h.serveExecutionConversations(ctx, w, r, runtime, parts)
		}
	case "calls":
		h.serveExecutionCalls(ctx, w, r, runtime, parts)
	case "permissions":
		h.serveExecutionPermissions(ctx, w, r, runtime, parts)
	case "approvals":
		h.serveExecutionApprovals(ctx, w, r, runtime, parts)
	default:
		writeRuntimeAPIError(w, 404, "NOT_FOUND", "execution route not found")
	}
}
