package httpx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/auth"
	"github.com/uvwt/agentdock/internal/mcp"
)

// Exercise the normal password consent, DCR, PKCE and MCP handlers over TLS.
// The persistent store and runtime are then recreated at the same origin, as on
// a Core restart. Only the isolated fixture file is read; no real user data is used.
func TestOAuthTLSMCPDiscoveryReadRefreshAndCoreRestart(t *testing.T) {
	const password = "isolated-consent-password"
	t.Setenv("AGENTDOCK_OAUTH_PASSWORD", password)
	t.Setenv("AGENTDOCK_OAUTH_TOKEN_SECRET", "isolated-integration-signing-secret")
	cfg := oauthTestConfig(t)
	var active atomic.Pointer[http.ServeMux]
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mux := active.Load(); mux != nil {
			mux.ServeHTTP(w, r)
		} else {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	cfg.OAuthServerURL = server.URL
	cfg.AuthToken = "isolated-static-token-not-used-for-oauth"
	fixture := filepath.Join(cfg.AgentDockDefaultDir, "oauth-read-fixture.txt")
	if err := os.WriteFile(fixture, []byte("oauth-read-flow-verified"), 0600); err != nil {
		t.Fatal(err)
	}
	var runtime *app.Runtime
	restart := func() {
		if runtime != nil {
			if err := runtime.Close(); err != nil {
				t.Fatal(err)
			}
		}
		store, err := auth.NewPersistentOAuthStore(filepath.Join(cfg.AgentDockHome, "oauth", "state-v1.json"), oauthSigningKey())
		if err != nil {
			t.Fatal(err)
		}
		runtime, err = app.NewRuntime(cfg)
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		registerOAuthRoutes(mux, cfg, store)
		mux.HandleFunc("/mcp", mcpEndpointHandler(mcp.NewServer(runtime, cfg), cfg, store))
		active.Store(mux)
	}
	restart()
	defer func() { runtime.Close() }()
	client := server.Client()
	client.Timeout = 10 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request := func(method, path, contentType, body, access string, expected int) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Accept", "application/json, text/event-stream")
		if access != "" {
			req.Header.Set("Authorization", "Bearer "+access)
		}
		if path == "/mcp" {
			req.Header.Set("MCP-Protocol-Version", "2025-03-26")
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s transport: %v", method, path, err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != expected {
			t.Fatalf("%s %s status %d, expected %d", method, strings.Split(path, "?")[0], response.StatusCode, expected)
		}
		return response, data
	}
	unauth, _ := request("GET", "/mcp", "", "", "", 401)
	metadata, err := auth.BearerResourceMetadata(unauth.Header.Values("WWW-Authenticate"))
	if err != nil || metadata != server.URL+"/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("invalid discovery: %v", err)
	}
	request("GET", "/.well-known/oauth-protected-resource/mcp", "", "", "", 200)
	request("GET", "/.well-known/oauth-authorization-server", "", "", "", 200)
	_, registered := request("POST", "/register", "application/json", `{"client_name":"Isolated OAuth integration","redirect_uris":["https://client.example/callback"],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"]}`, "", 201)
	var registration struct {
		ClientID string `json:"client_id"`
	}
	if json.Unmarshal(registered, &registration) != nil || registration.ClientID == "" {
		t.Fatal("DCR returned no client")
	}
	values := url.Values{
		"response_type": {"code"}, "client_id": {registration.ClientID}, "redirect_uri": {oauthTestRedirect},
		"code_challenge": {oauthTestChallenge}, "code_challenge_method": {"S256"},
		"resource": {server.URL + "/mcp"}, "state": {"integration-state"},
	}
	_, page := request("GET", "/oauth/authorize?"+values.Encode(), "", "", "", 200)
	if !strings.Contains(string(page), `type="password"`) {
		t.Fatal("normal authorization consent was bypassed")
	}
	values.Set("password", password)
	authorized, _ := request("POST", "/oauth/authorize", "application/x-www-form-urlencoded", values.Encode(), "", 302)
	callback, err := url.Parse(authorized.Header.Get("Location"))
	if err != nil || callback.Query().Get("state") != "integration-state" || callback.Query().Get("code") == "" {
		t.Fatal("invalid consent callback")
	}
	if callback.Scheme+"://"+callback.Host+callback.Path != oauthTestRedirect {
		t.Fatal("redirect binding changed")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {registration.ClientID}, "redirect_uri": {oauthTestRedirect},
		"code": {callback.Query().Get("code")}, "code_verifier": {oauthTestVerifier}, "resource": {server.URL + "/mcp"}}
	_, tokenBody := request("POST", "/oauth/token", "application/x-www-form-urlencoded", form.Encode(), "", 200)
	type tokens struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	var token tokens
	if json.Unmarshal(tokenBody, &token) != nil || token.Access == "" || token.Refresh == "" {
		t.Fatal("OAuth token exchange failed")
	}
	rpc := func(method string, params any, access string, id int) json.RawMessage {
		t.Helper()
		payload := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
		body, _ := json.Marshal(payload)
		_, data := request("POST", "/mcp", "application/json", string(body), access, 200)
		var result struct {
			ID     int             `json:"id"`
			Error  json.RawMessage `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(data, &result) != nil || result.ID != id || (len(result.Error) > 0 && string(result.Error) != "null") || len(result.Result) == 0 {
			t.Fatalf("MCP %s returned invalid RPC result", method)
		}
		return result.Result
	}
	checkMCP := func(access string) {
		initialized := rpc("initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "oauth-regression", "version": "1"}}, access, 1)
		if !strings.Contains(string(initialized), `"protocolVersion"`) {
			t.Fatal("initialization failed")
		}
		request("POST", "/mcp", "application/json", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, access, 202)
		definitions := rpc("tools/list", map[string]any{}, access, 2)
		var list struct {
			Tools []struct {
				Name        string          `json:"name"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		}
		if json.Unmarshal(definitions, &list) != nil || len(list.Tools) == 0 {
			t.Fatal("tools/list was empty")
		}
		found := false
		for _, tool := range list.Tools {
			if tool.Name == "read_file" && len(tool.InputSchema) > 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("read_file definition unavailable")
		}
		result := rpc("tools/call", map[string]any{"name": "read_file", "arguments": map[string]any{"path": fixture, "max_bytes": 4096}}, access, 3)
		var call struct {
			IsError bool `json:"isError"`
		}
		if json.Unmarshal(result, &call) != nil || call.IsError || !strings.Contains(string(result), "oauth-read-flow-verified") {
			t.Fatal("OAuth read-only tool call failed")
		}
	}
	checkMCP(token.Access)
	request("POST", "/mcp", "application/json", `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`, "invalid-token", 401)
	restart()
	checkMCP(token.Access)
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {registration.ClientID}, "refresh_token": {token.Refresh}, "resource": {server.URL + "/mcp"}}
	_, refreshBody := request("POST", "/oauth/token", "application/x-www-form-urlencoded", refresh.Encode(), "", 200)
	var renewed tokens
	if json.Unmarshal(refreshBody, &renewed) != nil || renewed.Refresh == "" || renewed.Refresh == token.Refresh {
		t.Fatal("persistent refresh rotation failed")
	}
	checkMCP(renewed.Access)
	t.Log("PASS: TLS, discovery, DCR, normal password consent, PKCE S256, initialize, initialized notification, tools/list, OAuth read_file, invalid-token rejection, persistent Core restart and refresh")
}
