package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/config"
)

func setOutputBudget(t *testing.T, r *Runtime, enabled bool, limit int) {
	t.Helper()
	_, err := r.RuntimeUpdateDisplaySettings(context.Background(), config.DisplayChange{ExpectedRevision: r.MCPPresentationSettings().Revision, ToolOutput: &config.ToolOutputSettings{Enabled: enabled, MaxChars: limit}})
	if err != nil {
		t.Fatal(err)
	}
}

func outputCall(t *testing.T, r *Runtime, ctx context.Context, name string, args map[string]any, body Result) Result {
	t.Helper()
	spec := ToolSpec{Name: name, Handler: func(context.Context, *Runtime, map[string]any) (Result, error) { return body, nil }}
	result, err := r.callObserved(ctx, spec, args)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestToolOutputSharedUnicodeBudgetAndDurableRead(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("output-owner")
	setOutputBudget(t, r, true, 1000)
	stdout := strings.Repeat("中😀e\u0301\r\n", 140)
	stderr := strings.Repeat("错误", 200)
	result := outputCall(t, r, ctx, "session_observe", map[string]any{"action": "list"}, Result{"stdout": stdout, "stderr": stderr, "status": "failed", "exit_code": 7, "command_ok": false})
	policy := result["output_policy"].(map[string]any)
	if policy["displayed_chars"] != 1000 || policy["truncated"] != true || result["exit_code"] != 7 || result["command_ok"] != false || utf8.RuneCountInString(stringArg(result, "stdout"))+utf8.RuneCountInString(stringArg(result, "stderr")) != 1000 {
		t.Fatalf("budget or control changed: %v", policy)
	}
	continuation, ok := policy["continue_read"].(map[string]any)
	if !ok {
		t.Fatalf("no readable continuation: %v", policy)
	}
	args := continuation["arguments"].(map[string]any)
	uri := stringArg(args, "path")
	var all strings.Builder
	offset := int64(0)
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("pagination did not terminate")
		}
		page, err := r.Call(ctx, "read_file", map[string]any{"path": uri, "offset": offset, "limit_chars": 1000})
		if err != nil {
			t.Fatal(err)
		}
		assertToolResultMatchestestOutputSchema(t, "read_file", page)
		text := stringArg(page, "content")
		if !utf8.ValidString(text) || utf8.RuneCountInString(text) > 1000 {
			t.Fatal("invalid scalar page")
		}
		next := page["next_offset"].(int64)
		if next-offset != int64(len(text)) {
			t.Fatal("offset is not consumed UTF-8 bytes")
		}
		all.WriteString(text)
		if page["has_more"] != true {
			break
		}
		if next <= offset {
			t.Fatal("page did not advance")
		}
		offset = next
	}
	var saved map[string]string
	if err := json.Unmarshal([]byte(all.String()), &saved); err != nil || saved["stdout"] != stdout || saved["stderr"] != stderr {
		t.Fatalf("retained source mismatch: %v", err)
	}
	call, err := r.activity.Call(ctx, stringArg(result, "call_id"))
	if err != nil || call.OutputSource == nil || call.Response == nil || call.OutputSource.Ref == call.Response.Ref {
		t.Fatal("returned envelope overwrote the original source")
	}
	_, err = r.Call(scopeHost("output-other-conversation"), "read_file", map[string]any{"path": uri})
	if err == nil {
		t.Fatal("unbound different conversation read protected output")
	}
	intruder := activity.WithSource(context.Background(), activity.Source{Principal: "another-owner", HostConversationID: "intruder"})
	_, err = r.Call(intruder, "read_file", map[string]any{"path": uri})
	if err == nil {
		t.Fatal("another owner read protected output")
	}
	_, err = r.Call(ctx, "read_file", map[string]any{"path": uri, "start_line": 1})
	if err == nil {
		t.Fatal("mixed offset units accepted")
	}
	if err = os.Remove(filepath.Join(r.cfg.AgentDockHome, "tasks", "activity", "payloads", call.OutputSource.Ref+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Call(ctx, "read_file", map[string]any{"path": uri}); err == nil {
		t.Fatal("expired blob claimed readable")
	}
}

func TestToolOutputBoundariesDisabledAndRulesExempt(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("output-boundaries")
	for _, limit := range []int{1000, 1733, 100000} {
		setOutputBudget(t, r, true, limit)
		for _, length := range []int{limit - 1, limit, limit + 1} {
			result := outputCall(t, r, ctx, "read_file", map[string]any{"path": "ordinary.txt"}, Result{"content": strings.Repeat("😀", length)})
			policy := result["output_policy"].(map[string]any)
			if utf8.RuneCountInString(stringArg(result, "content")) != min(limit, length) || policy["truncated"] != (length > limit) {
				t.Fatalf("boundary %d/%d failed", limit, length)
			}
		}
	}
	setOutputBudget(t, r, false, 1000)
	full := strings.Repeat("汉", 5000)
	result := outputCall(t, r, ctx, "read_file", map[string]any{"path": "ordinary.txt"}, Result{"content": full})
	if result["content"] != full || result["output_policy"] != nil {
		t.Fatal("disabled setting still truncated")
	}
	setOutputBudget(t, r, true, 1000)
	for _, path := range []string{"AGENTS.md", "sub/AGENTS.md", "skill://managed/example/SKILL.md"} {
		result := outputCall(t, r, ctx, "read_file", map[string]any{"path": path}, Result{"content": full})
		if result["content"] != full || result["output_policy"] != nil {
			t.Fatal("rule body was truncated")
		}
	}
	root := t.TempDir()
	writeInstructionFixture(t, root, full)
	contextResult, err := r.Call(ctx, "agentdock_context", map[string]any{"workdir": root})
	if err != nil {
		t.Fatal(err)
	}
	assertContextScope(t, contextResult, root, full)
	if contextResult["output_policy"] != nil {
		t.Fatal("bootstrap received an ordinary text limit")
	}
}

func TestToolOutputSnapshotHotChangeDoesNotAlterInFlight(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("output-hot-change")
	setOutputBudget(t, r, true, 1000)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan Result, 1)
	failed := make(chan error, 1)
	spec := ToolSpec{Name: "read_file", Handler: func(context.Context, *Runtime, map[string]any) (Result, error) {
		close(entered)
		<-release
		return Result{"content": strings.Repeat("A", 4000)}, nil
	}}
	go func() {
		result, err := r.callObserved(ctx, spec, map[string]any{"path": "ordinary.txt"})
		done <- result
		failed <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("fixture did not enter")
	}
	setOutputBudget(t, r, true, 3000)
	close(release)
	result := <-done
	if err := <-failed; err != nil {
		t.Fatal(err)
	}
	if len(stringArg(result, "content")) != 1000 {
		t.Fatal("in-flight policy changed")
	}
	result = outputCall(t, r, ctx, "read_file", map[string]any{"path": "ordinary.txt"}, Result{"content": strings.Repeat("B", 4000)})
	if len(stringArg(result, "content")) != 3000 {
		t.Fatal("subsequent call did not use updated policy")
	}
}

func TestToolOutputDynamicPlainTextAndUnknownStructuredContract(t *testing.T) {
	r := executionTestRuntime(t)
	p := &preparedExecution{spec: ToolSpec{Name: "mcp_tool_call"}, args: map[string]any{}, outputPolicy: config.ToolOutputSettings{Enabled: true, MaxChars: 1000}}
	// Pure formatter test: no journal reference is advertised without identity.
	first, second := strings.Repeat("A", 800), strings.Repeat("汉", 700)
	remote := map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": first}, map[string]any{"type": "image", "data": "abc", "mimeType": "image/png"}, map[string]any{"type": "text", "text": second}}}
	result := r.applyToolOutputPolicy(p, Result{"result": remote})
	limited := result["result"].(map[string]any)
	blocks := limited["content"].([]any)
	if limited["isError"] != true || utf8.RuneCountInString(blocks[2].(map[string]any)["text"].(string)) != 200 || blocks[1].(map[string]any)["data"] != "abc" {
		t.Fatal("dynamic content or status contract changed")
	}
	if remote["content"].([]any)[2].(map[string]any)["text"] != second {
		t.Fatal("formatter mutated a shared upstream result")
	}
	structured := map[string]any{"text": strings.Repeat("长", 2000), "status": "ok"}
	encoded, _ := json.Marshal(structured)
	remote = map[string]any{"structuredContent": structured, "content": []any{map[string]any{"type": "text", "text": string(encoded)}}}
	result = r.applyToolOutputPolicy(p, Result{"result": remote})
	if remote["structuredContent"].(map[string]any)["text"] != structured["text"] || result["output_policy"].(map[string]any)["unapplied_fields"] == nil {
		t.Fatal("unknown schema was silently altered")
	}
}

func TestToolOutputRedactionAndOriginalTruncationRemainExplicit(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("output-source-state")
	setOutputBudget(t, r, true, 1000)
	result := outputCall(t, r, ctx, "session_observe", map[string]any{"action": "list"}, Result{"stdout": "password=do-not-retain " + strings.Repeat("A", 1200), "stderr": "", "stdout_truncated": true, "status": "running"})
	policy := result["output_policy"].(map[string]any)
	if strings.Contains(stringArg(result, "stdout"), "do-not-retain") || policy["source_truncated"] != true || policy["storage_state"] != "partial" {
		t.Fatalf("source/redaction state lost: %v", policy)
	}
}
