package httpx

import (
	"context"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/taskstate"
	"net/http"
	"strconv"
)

// Resource dispatch shares the original authentication, deadline and JSON boundary.
func (h *activityHTTP) serveExecutionOverview(ctx context.Context, w http.ResponseWriter, r *http.Request, runtime executionRuntime, parts []string) {
	finish, require := executionResponse(w, r)
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
	writeRuntimeAPIError(w, 404, "NOT_FOUND", "execution route not found")
}
