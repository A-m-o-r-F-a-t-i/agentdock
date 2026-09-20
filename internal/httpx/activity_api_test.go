package httpx

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/auth"
)

func TestActivityDirectLoopbackGuard(t *testing.T) {
	for _, test := range []struct {
		name, remote, host, header, value string
		allow                             bool
	}{
		{name: "local ipv4", remote: "127.0.0.1:5000", host: "127.0.0.1:8765", allow: true},
		{name: "local ipv6", remote: "[::1]:5000", host: "[::1]:8765", allow: true},
		{name: "local name", remote: "127.0.0.1:5000", host: "localhost:8765", allow: true},
		{name: "public direct", remote: "198.51.100.9:5000", host: "127.0.0.1:8765"},
		{name: "dns rebinding", remote: "127.0.0.1:5000", host: "attacker.example"},
		{name: "cloudflare", remote: "127.0.0.1:5000", host: "localhost:8765", header: "CF-Connecting-IP", value: "203.0.113.5"},
		{name: "funnel", remote: "127.0.0.1:5000", host: "localhost:8765", header: "X-Forwarded-For", value: "203.0.113.5"},
		{name: "generic proxy", remote: "127.0.0.1:5000", host: "localhost:8765", header: "Forwarded", value: "for=203.0.113.5"},
		{name: "cross origin", remote: "127.0.0.1:5000", host: "localhost:8765", header: "Origin", value: "https://attacker.example"},
		{name: "cross site", remote: "127.0.0.1:5000", host: "localhost:8765", header: "Sec-Fetch-Site", value: "cross-site"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://localhost/internal/runtime/activity", nil)
			r.RemoteAddr, r.Host = test.remote, test.host
			if test.header != "" {
				r.Header.Set(test.header, test.value)
			}
			if got := directLoopbackRequest(r); got != test.allow {
				t.Fatalf("allowed=%v want=%v", got, test.allow)
			}
		})
	}
}

func TestActivityHTTPAuthorizationQueriesAndControls(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthToken = "local-test-token"
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	handler := runtimeAPIHandler(rt, cfg, auth.NewOAuthStore())
	request := func(method, path, body string, authorized bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:45678"
		if authorized {
			r.Header.Set("Authorization", "Bearer local-test-token")
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if got := request("GET", "/internal/runtime/activity", "", false); got.Code != 401 {
		t.Fatalf("unauthenticated %d %s", got.Code, got.Body.String())
	}
	if got := request("GET", "/internal/runtime/activity?after=bad", "", true); got.Code != 400 {
		t.Fatal(got.Code, got.Body.String())
	}
	if got := request("GET", "/internal/runtime/activity?limit=501", "", true); got.Code != 400 {
		t.Fatal(got.Code, got.Body.String())
	}
	created, err := rt.Call(context.Background(), "task_manage", map[string]any{"action": "create", "title": "HTTP task", "goal": "verify", "completion_conditions": []any{"passed"}})
	if err != nil {
		t.Fatal(err)
	}
	id := created["task_id"].(string)
	for _, path := range []string{"/internal/runtime/activity/tasks", "/internal/runtime/tasks/" + id + "/threads", "/internal/runtime/tasks/" + id + "/threads/main", "/internal/runtime/tasks/" + id + "/threads/main/activity", "/internal/runtime/activity?task_id=" + id} {
		got := request("GET", path, "", true)
		if got.Code != 200 {
			t.Fatalf("%s: %d %s", path, got.Code, got.Body.String())
		}
	}
	for _, body := range []string{fmt.Sprintf(`{"action":"cancel","task_id":%q,"summary":"cancelled from UI"}`, id), fmt.Sprintf(`{"action":"archive","task_id":%q}`, id)} {
		got := request("POST", "/internal/runtime/activity/control", body, true)
		if got.Code != 200 {
			t.Fatalf("control %d %s", got.Code, got.Body.String())
		}
	}
	hidden := request("GET", "/internal/runtime/activity/tasks", "", true)
	if strings.Contains(hidden.Body.String(), id) {
		t.Fatal("archived task still in active list")
	}
	history := request("GET", "/internal/runtime/activity/tasks?include_archived=true", "", true)
	if !strings.Contains(history.Body.String(), id) {
		t.Fatal("archive history lost")
	}
	for _, body := range []string{`{"action":"exec","cmd":"arbitrary"}`, `{"action":"cleanup","unknown":true}`, `{} {}`} {
		got := request("POST", "/internal/runtime/activity/control", body, true)
		if got.Code != 400 {
			t.Fatal(got.Code, got.Body.String())
		}
	}
}

func TestActivitySSEReplayReconnectAndLiveOutputThroughMiddleware(t *testing.T) {
	cfg := testConfig(t)
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	appendEvent := func(title string) activity.Event {
		t.Helper()
		event, err := rt.ActivityJournal().Append(context.Background(), activity.Event{Kind: "tool.completed", Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	first := appendEvent("first")
	second := appendEvent("second")
	server := httptest.NewServer(loggingMiddleware(runtimeAPIHandler(rt, cfg, auth.NewOAuthStore())))
	defer server.Close()
	connect := func(after uint64, header string) (*http.Response, context.CancelFunc) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/internal/runtime/activity/stream?after=%d", server.URL, after), nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if header != "" {
			req.Header.Set("Last-Event-ID", header)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			cancel()
			t.Fatalf("SSE %d %s", resp.StatusCode, data)
		}
		return resp, cancel
	}
	readEvent := func(scanner *bufio.Scanner) activity.Event {
		t.Helper()
		kind := ""
		data := ""
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if kind == "activity" {
					var event activity.Event
					if err := json.Unmarshal([]byte(data), &event); err != nil {
						t.Fatal(err)
					}
					return event
				}
				kind, data = "", ""
				continue
			}
			if strings.HasPrefix(line, "event: ") {
				kind = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		t.Fatalf("SSE ended without event: %v", scanner.Err())
		return activity.Event{}
	}
	response, cancel := connect(first.Seq, "")
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), activity.MaxEventBytes+512)
	if got := readEvent(scanner); got.Seq != second.Seq {
		t.Fatalf("initial replay=%+v", got)
	}
	third := appendEvent("live third")
	if got := readEvent(scanner); got.Seq != third.Seq {
		t.Fatalf("live event=%+v", got)
	}
	response.Body.Close()
	cancel()
	fourth := appendEvent("fourth")
	response, cancel = connect(0, fmt.Sprint(third.Seq))
	defer cancel()
	defer response.Body.Close()
	scanner = bufio.NewScanner(response.Body)
	if got := readEvent(scanner); got.Seq != fourth.Seq {
		t.Fatalf("Last-Event-ID replay duplicated: %+v", got)
	}
}

func TestActivityStreamLimitAndDisconnectRelease(t *testing.T) {
	cfg := testConfig(t)
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	h := &activityHTTP{runtime: rt, config: cfg, oauth: auth.NewOAuthStore()}
	h.streams.Store(maxActivityStreams)
	req := httptest.NewRequest("GET", "http://127.0.0.1/internal/runtime/activity/stream", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	if recorder.Code != 429 || h.streams.Load() != maxActivityStreams {
		t.Fatalf("limit=%d count=%d", recorder.Code, h.streams.Load())
	}
	h.streams.Store(0)
	server := httptest.NewServer(loggingMiddleware(h))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/internal/runtime/activity/stream", nil)
	response, err := server.Client().Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	response.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for h.streams.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.streams.Load() != 0 {
		t.Fatal("disconnected SSE retained a consumer slot")
	}
}

func TestActivityLiveRequiresLocalCredentialAndExplicitBinding(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthToken = "local-live-fixture"
	rt, err := app.NewRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	handler := runtimeAPIHandler(rt, cfg, auth.NewOAuthStore())
	for _, test := range []struct {
		remote, token, query string
		code                 int
	}{
		{"127.0.0.1:9000", "", "", 401},
		{"203.0.113.7:9000", cfg.AuthToken, "", 403},
		{"127.0.0.1:9000", cfg.AuthToken, "?thread_id=main", 400},
		{"127.0.0.1:9000", cfg.AuthToken, "", 200},
	} {
		request := httptest.NewRequest("GET", "http://127.0.0.1:8765/internal/runtime/activity/live"+test.query, nil)
		request.RemoteAddr = test.remote
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.code {
			t.Fatalf("live %s %s status %d, expected %d: %s", test.remote, test.query, response.Code, test.code, response.Body.String())
		}
		if test.code == 200 && !strings.Contains(response.Body.String(), `"workspace_status":"unbound"`) {
			t.Fatal("global live view inherited a workspace")
		}
	}
}
