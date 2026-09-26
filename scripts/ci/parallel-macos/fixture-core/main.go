// CI-only real Core fixture. It never installs services or uses an existing home.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/httpx"
	coremcp "github.com/uvwt/agentdock/internal/mcp"
)

const fixtureToken = "wb06-isolated-core-fixture-token"

type bearerTransport struct{}

func (bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+fixtureToken)
	return http.DefaultTransport.RoundTrip(copy)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if os.Getenv("GITHUB_ACTIONS") != "true" || len(os.Args) != 2 {
		return errors.New("this isolated fixture is restricted to GitHub Actions; pass the ready JSON path")
	}
	root, err := os.MkdirTemp("", "wb06-isolated-core-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cfg := config.Config{AgentDockDefaultDir: root, AgentDockHome: filepath.Join(root, "home"),
		Host: "127.0.0.1", Port: port, AuthToken: fixtureToken, ContextTimeoutMS: 30000}
	if err := cfg.Normalize(); err != nil {
		return err
	}
	runtime, err := app.NewRuntime(cfg)
	if err != nil {
		return err
	}
	defer runtime.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	serverErr := make(chan error, 1)
	go func() { serverErr <- httpx.Serve(ctx, coremcp.NewServer(runtime, cfg), runtime, cfg) }()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Transport: bearerTransport{}, Timeout: 30 * time.Second}
	ready := false
	for i := 0; i < 100; i++ {
		response, failure := client.Get(baseURL + "/healthz")
		if failure == nil {
			ready = response.StatusCode == 200
			response.Body.Close()
			if ready {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		return errors.New("isolated Core readiness failed")
	}
	agent := sdk.NewClient(&sdk.Implementation{Name: "WB06-native-integration-fixture", Version: "1"}, nil)
	session, err := agent.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: baseURL + "/mcp", HTTPClient: client}, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	call := func(host, name string, args map[string]any) (app.Result, error) {
		meta := sdk.Meta{}
		if host != "" {
			meta["openai/session"] = host
		}
		result, failure := session.CallTool(ctx, &sdk.CallToolParams{Meta: meta, Name: name, Arguments: args})
		if failure != nil {
			return nil, failure
		}
		data, failure := json.Marshal(result.StructuredContent)
		if failure != nil {
			return nil, failure
		}
		var value app.Result
		if failure = json.Unmarshal(data, &value); failure != nil {
			return nil, failure
		}
		if result.IsError {
			return nil, fmt.Errorf("fixture %s failed: %v", name, value)
		}
		return value, nil
	}
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte("WB06 fixture 中文 😀 e\u0301\n"), 0600); err != nil {
		return err
	}
	a, err := call("native-fixture-A", "task_manage", map[string]any{"action": "create", "title": "Native Core task", "goal": "Native client verification", "completion_conditions": []string{"verified"}, "steps": []map[string]any{{"id": "S1", "title": "Check client"}}})
	if err != nil {
		return err
	}
	b, err := call("native-fixture-B", "agentdock_context", map[string]any{})
	if err != nil {
		return err
	}
	read, err := call("native-fixture-A", "read_file", map[string]any{"path": "fixture.txt"})
	if err != nil {
		return err
	}
	pending, err := call("native-fixture-A", "file_edit", map[string]any{"action": "add", "path": "approved.txt", "content": "native client approved fixed request"})
	if err != nil {
		return err
	}
	if pending["status"] != "pending_approval" {
		return fmt.Errorf("fixture requires default guarded policy, got %v", pending["status"])
	}
	if _, err = call("", "list_dir", map[string]any{"path": ".", "max_entries": 2}); err != nil {
		return err
	}
	identity := map[string]any{"base_url": baseURL, "token": fixtureToken, "conversation_a": a["conversation_id"],
		"conversation_b": b["conversation_id"], "task_id": a["task_id"], "read_call_id": read["call_id"],
		"approval_id": pending["approval_id"], "approval_call_id": pending["call_id"], "simulated_host": true}
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	if err = os.WriteFile(os.Args[1], data, 0600); err != nil {
		return err
	}
	fmt.Println("isolated real Core fixture ready; no installed service or production home used")
	select {
	case <-ctx.Done():
		return nil
	case err := <-serverErr:
		return err
	}
}
