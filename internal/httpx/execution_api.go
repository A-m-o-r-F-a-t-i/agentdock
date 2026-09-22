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
	"github.com/uvwt/agentdock/internal/config"
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
	if len(parts) == 2 && parts[0] == "execution" && parts[1] == "notifications" {
		if !require("POST") {
			return
		}
		var input struct {
			Limit int `json:"limit"`
		}
		if !decodeExecutionBody(w, r, &input) {
			return
		}
		service, ok := h.runtime.(interface {
			RuntimeCompletionNotifications(context.Context, int) (app.Result, error)
		})
		if !ok {
			writeRuntimeAPIError(w, 503, "NOTIFICATIONS_UNAVAILABLE", "task notifications unavailable")
			return
		}
		result, err := service.RuntimeCompletionNotifications(ctx, input.Limit)
		finish(result, err)
		return
	}
	if len(parts) == 2 && parts[0] == "execution" && parts[1] == "sidebar" {
		if !require("POST") {
			return
		}
		var request app.SidebarRequest
		if !decodeExecutionBody(w, r, &request) {
			return
		}
		observer, ok := h.runtime.(interface {
			RuntimeConversationSidebar(context.Context, app.SidebarRequest) (app.SidebarPage, error)
		})
		if !ok {
			writeRuntimeAPIError(w, 503, "SIDEBAR_UNAVAILABLE", "sidebar projection unavailable")
			return
		}
		result, err := observer.RuntimeConversationSidebar(ctx, request)
		finish(result, err)
		return
	}
	if len(parts) == 2 && parts[0] == "execution" && parts[1] == "connection" {
		if !require("GET") {
			return
		}
		observer, ok := h.runtime.(interface {
			RuntimeClientConnection(context.Context) (app.Result, error)
		})
		if !ok {
			writeRuntimeAPIError(w, 503, "CONNECTION_UNAVAILABLE", "client observation unavailable")
			return
		}
		result, err := observer.RuntimeClientConnection(ctx)
		finish(result, err)
		return
	}
	if len(parts) == 2 && parts[0] == "execution" && parts[1] == "display" {
		display, ok := h.runtime.(interface {
			RuntimeDisplaySettings(context.Context) (app.Result, error)
			RuntimeUpdateDisplaySettings(context.Context, config.DisplayChange) (app.Result, error)
		})
		if !ok {
			writeRuntimeAPIError(w, 503, "DISPLAY_UNAVAILABLE", "display settings are unavailable")
			return
		}
		if r.Method == http.MethodGet {
			result, err := display.RuntimeDisplaySettings(ctx)
			finish(result, err)
			return
		}
		if r.Method != http.MethodPost {
			activityMethodError(w, "GET, POST")
			return
		}
		var change config.DisplayChange
		if !decodeExecutionBody(w, r, &change) {
			return
		}
		result, err := display.RuntimeUpdateDisplaySettings(ctx, change)
		finish(result, err)
		return
	}
	if len(parts) == 1 && parts[0] == "execution" {
		if !require("GET") {
			return
		}
		result, err := runtime.RuntimeExecutionOverview(ctx)
		finish(result, err)
		return
	}
	if len(parts) == 2 && parts[0] == "execution" && parts[1] == "tasks" {
		if !require("GET") {
			return
		}
		offset, limit, err := executionPaging(r)
		if err != nil {
			executionError(w, err)
			return
		}
		query := r.URL.Query()
		selection := false
		if value := query.Get("selection"); value != "" {
			selection, err = strconv.ParseBool(value)
			if err != nil {
				executionError(w, err)
				return
			}
		}
		page, err := runtime.RuntimeManagedTasks(ctx, taskstate.TaskQuery{View: query.Get("view"), Status: query.Get("status"), WorkspaceID: query.Get("workspace_id"), Tag: query.Get("tag"), Search: query.Get("search"), Offset: offset, Limit: limit, Selection: selection})
		finish(page, err)
		return
	}
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
	if parts[0] == "calls" {
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
