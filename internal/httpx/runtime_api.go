package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/auth"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/runtimeapi"
)

func registerRuntimeAPI(mux *http.ServeMux, runtime runtimeapi.Runtime, cfg config.Config, oauthStore *auth.OAuthStore) {
	h := runtimeAPIHandler(runtime, cfg, oauthStore)
	mux.HandleFunc("/internal/runtime/status", h)
	mux.HandleFunc("/internal/runtime/capabilities", h)
	mux.HandleFunc("/internal/runtime/skills", h)
	mux.HandleFunc("/internal/runtime/skills/", h)
	mux.HandleFunc("/internal/runtime/plugins", h)
	mux.HandleFunc("/internal/runtime/plugins/", h)
	mux.HandleFunc("/internal/runtime/tasks", h)
	mux.HandleFunc("/internal/runtime/tasks/", h)
	for _, path := range []string{"execution", "conversations", "calls", "permissions", "approvals"} {
		mux.HandleFunc("/internal/runtime/"+path, h)
		mux.HandleFunc("/internal/runtime/"+path+"/", h)
	}
	mux.HandleFunc("/internal/runtime/activity", h)
	mux.HandleFunc("/internal/runtime/activity/", h)
	mux.HandleFunc("/internal/runtime/evolve", h)
	mux.HandleFunc("/internal/runtime/mcp", h)
	mux.HandleFunc("/internal/runtime/mcp/", h)
}

func runtimeAPIHandler(runtime runtimeapi.Runtime, cfg config.Config, oauthStore *auth.OAuthStore) http.HandlerFunc {
	localRuntime, _ := runtime.(activityRuntime)
	localActivity := &activityHTTP{runtime: localRuntime, config: cfg, oauth: oauthStore}
	authorizer := auth.Bearer{Token: cfg.AuthToken}
	authRequired := cfg.AuthRequired()
	return func(w http.ResponseWriter, r *http.Request) {
		managementPath := strings.TrimSuffix(r.URL.Path, "/")
		managementWrite := managementPath == "/internal/runtime/skills" || managementPath == "/internal/runtime/plugins" || managementPath == "/internal/runtime/mcp" || managementPath == "/internal/runtime/evolve" || managementPath == "/internal/runtime/workflow-templates"
		if r.Method == http.MethodPost && managementWrite && !directLoopbackRequest(r) {
			writeRuntimeAPIError(w, 403, "LOCAL_ONLY", "runtime management writes require a direct authenticated local client")
			return
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/internal/runtime/tasks/") && !directLoopbackRequest(r) {
			writeRuntimeAPIError(w, 403, "LOCAL_ONLY", "task deletion requires a direct local client")
			return
		}
		if isActivityRoute(r.URL.Path) || isExecutionRoute(r.URL.Path) {
			localActivity.ServeHTTP(w, r)
			return
		}
		if !runtimeapi.MethodAllowed(r.Method, r.URL.Path) {
			w.Header().Set("Allow", runtimeapi.AllowHeader(r.URL.Path))
			writeRuntimeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		staticOK := cfg.AuthToken != "" && authorizer.Authorized(r)
		principal, oauthOK := oauthExecutionPrincipal(r, cfg, oauthStore)
		if authRequired && !staticOK && !oauthOK {
			setBearerChallenge(w, cfg, r, strings.TrimSpace(r.Header.Get("Authorization")) != "")
			writeRuntimeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
			return
		}

		if staticOK {
			principal = "http:static"
		} else if !oauthOK {
			principal = "http:local"
		}
		sourceCtx := activity.WithSource(r.Context(), activity.Source{Principal: principal, Provider: "local-ui", Namespace: "http:runtime"})
		if r.Method == http.MethodGet {
			sourceCtx = activity.WithDiagnostic(sourceCtx)
		}
		r = r.WithContext(sourceCtx)
		body, err := runtimeRequestBody(r)
		if err != nil {
			writeRuntimeAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "failed to read runtime request body")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		result, err := runtimeapi.Dispatch(ctx, runtime, runtimeapi.Request{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Body:   body,
		})
		if err != nil {
			writeRuntimeAPIHandlerError(w, err)
			return
		}
		writeJSON(w, result)
	}
}

func runtimeRequestBody(r *http.Request) ([]byte, error) {
	if r.Method != http.MethodPost {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(r.Body, 64*1024+1))
}

func writeRuntimeAPIHandlerError(w http.ResponseWriter, err error) {
	var toolErr *app.ToolError
	if errors.As(err, &toolErr) {
		status := http.StatusInternalServerError
		switch toolErr.Category {
		case "validation":
			status = http.StatusBadRequest
		case "conflict":
			status = http.StatusConflict
		case "permission":
			status = http.StatusForbidden
		case "not_found":
			status = http.StatusNotFound
		}
		writeRuntimeAPIError(w, status, toolErr.Code, toolErr.Message)
		return
	}
	writeRuntimeAPIError(w, http.StatusInternalServerError, "RUNTIME_API_ERROR", err.Error())
}

func writeRuntimeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{"ok": false, "code": code, "error": message}); err != nil {
		slog.Warn("write runtime API error response failed", "status", status, "code", code, "error", err)
	}
}
