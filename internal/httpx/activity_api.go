package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/auth"
	"github.com/uvwt/agentdock/internal/config"
)

const maxActivityStreams = 32

type activityRuntime interface {
	ActivityJournal() *activity.Store
	RuntimeActivityTask(context.Context, map[string]any) (app.Result, error)
	RuntimeActivityControl(context.Context, app.ActivityControlRequest) (app.Result, error)
	RuntimeActivityDiff(context.Context, app.ActivityDiffRequest) (app.Result, error)
}

type activityHTTP struct {
	runtime activityRuntime
	config  config.Config
	oauth   *auth.OAuthStore
	streams atomic.Int32
}

func isActivityRoute(path string) bool {
	if path == "/internal/runtime/activity" || strings.HasPrefix(path, "/internal/runtime/activity/") {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/internal/runtime/"), "/")
	return len(parts) >= 3 && parts[0] == "tasks" && (parts[2] == "threads" || parts[2] == "activity")
}

// Direct loopback, authority, origin and proxy checks are all required. Tunnel reverse
// proxies often connect from loopback themselves, so RemoteAddr alone is insufficient.
func directLoopbackRequest(r *http.Request) bool {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(remote) == nil || !net.ParseIP(remote).IsLoopback() {
		return false
	}
	authority := r.Host
	if host, _, err := net.SplitHostPort(authority); err == nil {
		authority = host
	}
	authority = strings.Trim(authority, "[]")
	if !strings.EqualFold(authority, "localhost") {
		ip := net.ParseIP(authority)
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	for key := range r.Header {
		lower := strings.ToLower(key)
		if lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "cf-") || strings.HasPrefix(lower, "tailscale-") || lower == "x-real-ip" {
			return false
		}
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.User != nil || parsed.Host != r.Host || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return false
		}
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	return true
}

func (h *activityHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !directLoopbackRequest(r) {
		writeRuntimeAPIError(w, http.StatusForbidden, "LOCAL_ONLY", "task activity is available only to direct loopback clients")
		return
	}
	bearer := auth.Bearer{Token: h.config.AuthToken}
	if h.config.AuthRequired() && !(h.config.AuthToken != "" && bearer.Authorized(r)) && !authorizedOAuth(r, h.config, h.oauth) {
		writeRuntimeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if h.runtime == nil || h.runtime.ActivityJournal() == nil {
		writeRuntimeAPIError(w, http.StatusServiceUnavailable, "ACTIVITY_UNAVAILABLE", "activity service is unavailable")
		return
	}
	path := r.URL.Path
	if path == "/internal/runtime/activity/control" {
		if r.Method != http.MethodPost {
			activityMethodError(w, "POST")
			return
		}
		h.control(w, r)
		return
	}
	if r.Method != http.MethodGet {
		activityMethodError(w, "GET")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if path == "/internal/runtime/activity/diff" {
		seq, err := strconv.ParseUint(r.URL.Query().Get("seq"), 10, 64)
		if err != nil || seq == 0 {
			writeRuntimeAPIError(w, 400, "INVALID_CURSOR", "a positive file event sequence is required")
			return
		}
		result, err := h.runtime.RuntimeActivityDiff(ctx, app.ActivityDiffRequest{TaskID: r.URL.Query().Get("task_id"), ThreadID: r.URL.Query().Get("thread_id"), Seq: seq})
		if err != nil {
			writeRuntimeAPIHandlerError(w, err)
			return
		}
		writeJSON(w, result)
		return
	}
	if path == "/internal/runtime/activity/tasks" {
		limit, err := activityLimit(r, 200)
		if err != nil {
			writeRuntimeAPIError(w, 400, "INVALID_LIMIT", err.Error())
			return
		}
		args := map[string]any{"action": "list", "limit": limit}
		if status := r.URL.Query().Get("status"); status != "" {
			args["status"] = status
		}
		if value := r.URL.Query().Get("include_archived"); value != "" {
			flag, err := strconv.ParseBool(value)
			if err != nil {
				writeRuntimeAPIError(w, 400, "INVALID_ARGUMENT", "include_archived must be boolean")
				return
			}
			args["include_archived"] = flag
		}
		result, err := h.runtime.RuntimeActivityTask(ctx, args)
		if err != nil {
			writeRuntimeAPIHandlerError(w, err)
			return
		}
		writeJSON(w, result)
		return
	}
	query := activity.Query{TaskID: r.URL.Query().Get("task_id"), ThreadID: r.URL.Query().Get("thread_id")}
	streaming := path == "/internal/runtime/activity/stream"
	parts := strings.Split(strings.TrimPrefix(path, "/internal/runtime/"), "/")
	switch {
	case path == "/internal/runtime/activity" || path == "/internal/runtime/activity/stream":
	case len(parts) >= 3 && parts[0] == "tasks":
		query.TaskID = parts[1]
		switch {
		case len(parts) == 3 && parts[2] == "threads":
			result, err := h.runtime.RuntimeActivityTask(ctx, map[string]any{"action": "thread_list", "task_id": query.TaskID})
			if err != nil {
				writeRuntimeAPIHandlerError(w, err)
				return
			}
			writeJSON(w, result)
			return
		case len(parts) == 4 && parts[2] == "threads":
			result, err := h.runtime.RuntimeActivityTask(ctx, map[string]any{"action": "thread_get", "task_id": query.TaskID, "thread_id": parts[3]})
			if err != nil {
				writeRuntimeAPIHandlerError(w, err)
				return
			}
			writeJSON(w, result)
			return
		case len(parts) == 5 && parts[2] == "threads" && parts[4] == "activity":
			query.ThreadID = parts[3]
		case len(parts) == 3 && parts[2] == "activity":
		case len(parts) == 4 && parts[2] == "activity" && parts[3] == "stream":
			streaming = true
		default:
			writeRuntimeAPIError(w, 404, "NOT_FOUND", "activity route not found")
			return
		}
	default:
		writeRuntimeAPIError(w, 404, "NOT_FOUND", "activity route not found")
		return
	}
	if err := (activity.Binding{TaskID: query.TaskID, ThreadID: query.ThreadID}).Validate(); err != nil {
		writeRuntimeAPIError(w, 400, "INVALID_BINDING", err.Error())
		return
	}
	if query.TaskID != "" {
		args := map[string]any{"action": "get", "task_id": query.TaskID}
		if query.ThreadID != "" {
			args["thread_id"] = query.ThreadID
		}
		if _, err := h.runtime.RuntimeActivityTask(ctx, args); err != nil {
			writeRuntimeAPIHandlerError(w, err)
			return
		}
	}
	var err error
	query.Limit, err = activityLimit(r, activity.MaxQueryEvents)
	if err != nil {
		writeRuntimeAPIError(w, 400, "INVALID_LIMIT", err.Error())
		return
	}
	cursor := r.URL.Query().Get("after")
	if streaming && r.Header.Get("Last-Event-ID") != "" {
		cursor = r.Header.Get("Last-Event-ID")
	}
	if cursor != "" {
		query.After, err = strconv.ParseUint(cursor, 10, 64)
		if err != nil {
			writeRuntimeAPIError(w, 400, "INVALID_CURSOR", "activity cursor must be an unsigned sequence number")
			return
		}
	}
	if streaming {
		h.stream(w, r, query)
		return
	}
	page, err := h.runtime.ActivityJournal().Query(ctx, query)
	if err != nil {
		writeRuntimeAPIHandlerError(w, err)
		return
	}
	writeJSON(w, page)
}

func activityLimit(r *http.Request, maximum int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return maximum, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maximum {
		return 0, errors.New("limit is outside the supported page range")
	}
	return n, nil
}
func activityMethodError(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeRuntimeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
}

func (h *activityHTTP) control(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeRuntimeAPIError(w, 415, "JSON_REQUIRED", "control requests require application/json")
		return
	}
	reader := http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var request app.ActivityControlRequest
	if err = decoder.Decode(&request); err != nil {
		writeRuntimeAPIError(w, 400, "INVALID_ARGUMENT", "invalid activity control request")
		return
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		writeRuntimeAPIError(w, 400, "INVALID_ARGUMENT", "one JSON request is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := h.runtime.RuntimeActivityControl(ctx, request)
	if err != nil {
		writeRuntimeAPIHandlerError(w, err)
		return
	}
	writeJSON(w, result)
}
