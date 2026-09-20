package desktopruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type tailscaleTestTransport func(*http.Request) (*http.Response, error)

func (transport tailscaleTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func tailscaleProtocolFixture(t *testing.T, change func(http.ResponseWriter, *http.Request) bool) (*http.Client, *int) {
	t.Helper()
	origin := "https://device.example-tailnet.ts.net"
	initialized := new(int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if change != nil && change(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/healthz":
			fmt.Fprint(w, `{"status":"ok"}`)
		case "/mcp":
			if r.Method == http.MethodGet || r.Header.Get("Authorization") != "Bearer test-token" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+origin+`/.well-known/oauth-protected-resource/mcp"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Method == http.MethodDelete {
				if r.Header.Get("Mcp-Session-Id") != "probe-session" {
					t.Error("wrong session deleted")
				}
				w.WriteHeader(http.StatusOK)
				return
			}
			method := tailscaleFixtureMethod(r)
			switch method {
			case "initialize":
				*initialized++
				w.Header().Set("Mcp-Session-Id", "probe-session")
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":771,"result":{"protocolVersion":"2025-03-26","serverInfo":{"name":"AgentDock"}}}`)
			case "notifications/initialized", "tools/list":
				if r.Header.Get("Mcp-Session-Id") != "probe-session" || r.Header.Get("MCP-Protocol-Version") != "2025-03-26" {
					t.Error("missing negotiated session or protocol")
				}
				if method == "notifications/initialized" {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":772,"result":{"tools":[{"name":"list_dir","inputSchema":{"type":"object"}}]}}`)
			default:
				t.Errorf("unexpected MCP method %s", method)
				w.WriteHeader(400)
			}
		case "/internal/runtime/status":
			w.WriteHeader(http.StatusUnauthorized)
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": origin, "authorization_endpoint": origin + "/oauth/authorize", "token_endpoint": origin + "/oauth/token", "registration_endpoint": origin + "/register"})
		case "/.well-known/oauth-protected-resource/mcp":
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": origin + "/mcp", "authorization_servers": []string{origin}})
		case "/.well-known/mcp.json", "/.well-known/mcp/server-card.json":
			fmt.Fprint(w, `{"name":"AgentDock"}`)
		case "/register", "/oauth/token":
			w.WriteHeader(http.StatusMethodNotAllowed)
		case "/oauth/authorize":
			w.WriteHeader(http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := newTailscaleHTTPClient()
	client.Transport = tailscaleTestTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "device.example-tailnet.ts.net" {
			t.Errorf("unexpected origin: %s", request.URL.Host)
		}
		clone := request.Clone(request.Context())
		copiedURL := *request.URL
		clone.URL = &copiedURL
		clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(clone)
	})
	return client, initialized
}

func TestTailscalePublicProtocolVerification(t *testing.T) {
	client, initialized := tailscaleProtocolFixture(t, nil)
	if err := waitTailscalePublicOrigin(context.Background(), "https://device.example-tailnet.ts.net", "test-token", client); err != nil {
		t.Fatal(err)
	}
	if *initialized != 1 {
		t.Fatalf("initialize count %d", *initialized)
	}
}

func TestTailscalePublicProtocolRejectsSecurityFailures(t *testing.T) {
	for _, stage := range []string{"challenge", "runtime_auth", "metadata", "redirect", "initialize"} {
		t.Run(stage, func(t *testing.T) {
			client, _ := tailscaleProtocolFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
				switch {
				case stage == "challenge" && r.URL.Path == "/mcp" && r.Method == http.MethodGet:
					w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://old.example.test/meta"`)
					w.WriteHeader(401)
					return true
				case stage == "runtime_auth" && r.URL.Path == "/internal/runtime/status":
					w.WriteHeader(200)
					return true
				case stage == "metadata" && r.URL.Path == "/.well-known/oauth-authorization-server":
					fmt.Fprint(w, `{"issuer":"https://old.example.test"}`)
					return true
				case stage == "redirect" && r.URL.Path == "/mcp" && r.Method == http.MethodPost:
					w.Header().Set("Location", "https://foreign.example.test/mcp")
					w.WriteHeader(307)
					return true
				case stage == "initialize" && r.URL.Path == "/mcp" && r.Method == http.MethodPost:
					fmt.Fprint(w, `{"jsonrpc":"2.0","id":771,"error":{"code":-32600}}`)
					return true
				}
				return false
			})
			if err := waitTailscalePublicOrigin(context.Background(), "https://device.example-tailnet.ts.net", "test-token", client); err == nil {
				t.Fatal("accepted invalid protocol")
			}
		})
	}
}

func TestTailscaleInitializeSSEAndBoundedResponses(t *testing.T) {
	client, _ := tailscaleProtocolFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/mcp" && r.Method == http.MethodPost && tailscaleFixtureMethod(r) == "initialize" {
			w.Header().Set("Mcp-Session-Id", "probe-session")
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":771,\"result\":{\"protocolVersion\":\"2025-03-26\",\"serverInfo\":{\"name\":\"AgentDock\"}}}\n\n")
			return true
		}
		return false
	})
	if err := waitTailscalePublicOrigin(context.Background(), "https://device.example-tailnet.ts.net", "test-token", client); err != nil {
		t.Fatal(err)
	}
	client, _ = tailscaleProtocolFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/healthz" {
			fmt.Fprint(w, strings.Repeat("x", tailscaleJSONLimit+1))
			return true
		}
		return false
	})
	if err := waitTailscalePublicOrigin(context.Background(), "https://device.example-tailnet.ts.net", "test-token", client); tailscaleDiagnosticCode(err) != "response_limit" {
		t.Fatal(err)
	}
}

func TestTailscalePublicVerificationCancellation(t *testing.T) {
	client, _ := tailscaleProtocolFixture(t, func(w http.ResponseWriter, r *http.Request) bool { w.WriteHeader(503); return true })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := waitTailscalePublicOrigin(ctx, "https://device.example-tailnet.ts.net", "test-token", client); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func tailscaleFixtureMethod(r *http.Request) string {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	var request struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &request)
	return request.Method
}

func TestTailscaleChallengeFormattingAndToolDiscovery(t *testing.T) {
	client, _ := tailscaleProtocolFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/mcp" && r.Method == http.MethodGet {
			w.Header().Set("WWW-Authenticate", `Bearer scope="mcp", resource_metadata = "https://device.example-tailnet.ts.net/.well-known/oauth-protected-resource/mcp", error_description="login, then retry"`)
			w.WriteHeader(401)
			return true
		}
		return false
	})
	if err := verifyTailscalePublicOrigin(context.Background(), "https://device.example-tailnet.ts.net", "test-token", client); err != nil {
		t.Fatal(err)
	}
	client, _ = tailscaleProtocolFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/mcp" && r.Method == http.MethodPost && tailscaleFixtureMethod(r) == "tools/list" {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":772,"result":{"tools":[]}}`)
			return true
		}
		return false
	})
	if err := verifyTailscalePublicOrigin(context.Background(), "https://device.example-tailnet.ts.net", "test-token", client); tailscaleDiagnosticCode(err) != "tools_list_failed" {
		t.Fatalf("tools/list not verified: %v", err)
	}
}
