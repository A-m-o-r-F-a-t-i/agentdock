package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/permission"
)

func profileRuntime(t *testing.T, settings permission.Settings, mode string) (*Runtime, context.Context) {
	t.Helper()
	r := executionTestRuntime(t)
	ctx := scopeHost("profile-fixture")
	if _, err := r.Call(ctx, "agentdock_context", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.permissions.Update(ctx, permission.Change{Scope: "global", Mode: mode, ConfirmFull: mode == permission.Full, ExpectedRevision: 1, Settings: &settings}); err != nil {
		t.Fatal(err)
	}
	return r, ctx
}

func TestExecutionProfileFilesystemBoundaryAndOpaqueCommands(t *testing.T) {
	settings := permission.DefaultSettings()
	settings.Profile = permission.Profile{Filesystem: permission.FileWrite, Network: permission.Deny, SandboxBoundary: permission.BoundaryWorkspace}
	r, ctx := profileRuntime(t, settings, permission.Full)
	result, err := r.Call(ctx, "file_edit", map[string]any{"action": "add", "path": "inside.txt", "content": "inside"})
	if err != nil || resultReportsFailure(result) {
		t.Fatalf("contained native write failed %+v %v", result, err)
	}
	if _, err = r.Call(ctx, "read_file", map[string]any{"path": "inside.txt"}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err = os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{{"path": outside}, {"path": "../outside.txt"}} {
		if _, err = r.Call(ctx, "read_file", args); err == nil {
			t.Fatal("out-of-bound read admitted")
		}
	}
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"exec_command", map[string]any{"cmd": "echo harmless-looking"}},
		{"mcp_tool_call", map[string]any{"name": "remote:read", "arguments": map[string]any{}}},
		{"file_edit", map[string]any{"action": "add", "path": filepath.Join(r.cfg.AgentDockHome, "owned.txt"), "content": "must not write control state"}},
	} {
		if _, err = r.Call(WithLocalUserAction(ctx), call.name, call.args); err == nil {
			t.Fatalf("local action bypassed profile: %s", call.name)
		}
	}
	if _, err = os.Stat(filepath.Join(r.cfg.AgentDockHome, "owned.txt")); !os.IsNotExist(err) {
		t.Fatal("control state write happened")
	}
}

func TestExecutionProfileReadAndDenyModes(t *testing.T) {
	for _, fs := range []string{permission.FileRead, permission.Deny} {
		t.Run(fs, func(t *testing.T) {
			settings := permission.DefaultSettings()
			settings.Profile.Filesystem = fs
			r, ctx := profileRuntime(t, settings, permission.Full)
			if err := os.WriteFile(filepath.Join(r.cfg.AgentDockDefaultDir, "read.txt"), []byte("read"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := r.Call(ctx, "read_file", map[string]any{"path": "read.txt"})
			if (err != nil) != (fs == permission.Deny) {
				t.Fatalf("read mode mismatch: %v", err)
			}
			if _, err = r.Call(ctx, "file_edit", map[string]any{"action": "add", "path": "denied.txt", "content": "no"}); err == nil {
				t.Fatal("write admitted")
			}
			if _, err = os.Stat(filepath.Join(r.cfg.AgentDockDefaultDir, "denied.txt")); !os.IsNotExist(err) {
				t.Fatal("denied file exists")
			}
		})
	}
}

func TestExecutionProfileSymlinkEscape(t *testing.T) {
	settings := permission.DefaultSettings()
	settings.Profile.SandboxBoundary = permission.BoundaryWorkspace
	r, ctx := profileRuntime(t, settings, permission.Full)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(r.cfg.AgentDockDefaultDir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if _, err := r.Call(ctx, "read_file", map[string]any{"path": filepath.Join(link, "secret.txt")}); err == nil {
		t.Fatal("symlink escaped profile")
	}
}

func TestExecutionProfileNeverDoesNotQueueOrWrite(t *testing.T) {
	settings := permission.DefaultSettings()
	settings.Approval.Mode = permission.Never
	r, ctx := profileRuntime(t, settings, permission.Rules)
	if _, err := r.Call(WithLocalUserAction(ctx), "file_edit", map[string]any{"action": "add", "path": "never.txt", "content": "no"}); err == nil {
		t.Fatal("never bypassed by local shortcut")
	}
	approvals, err := r.permissions.Approvals(ctx)
	if err != nil || len(approvals) != 0 {
		t.Fatalf("never queued approval %+v %v", approvals, err)
	}
	if _, err = os.Stat(filepath.Join(r.cfg.AgentDockDefaultDir, "never.txt")); !os.IsNotExist(err) {
		t.Fatal("never wrote file")
	}
}

func TestPermissionAutoReviewerProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	var request permission.ReviewRequest
	if json.NewDecoder(os.Stdin).Decode(&request) != nil {
		os.Exit(10)
	}
	if len(request.FixedRequest) == 0 || !request.Redacted {
		os.Exit(11)
	}
	mode := os.Args[len(os.Args)-1]
	_ = json.NewEncoder(os.Stdout).Encode(permission.ReviewResult{ApprovalID: request.Approval.ID, CallID: request.Approval.CallID, Decision: mode, Reason: "isolated integration reviewer"})
	os.Exit(0)
}

func TestExecutionAutoReviewReturnsActualResultAndKeepsCall(t *testing.T) {
	for _, mode := range []string{"approve", "reject", "missing"} {
		t.Run(mode, func(t *testing.T) {
			settings := permission.DefaultSettings()
			settings.Reviewer = permission.ReviewerAuto
			r, ctx := profileRuntime(t, settings, permission.Rules)
			if mode != "missing" {
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(permission.ReviewerConfig{Command: exe, Args: []string{"-test.run=^TestPermissionAutoReviewerProcess$", "--", mode}, TimeoutMS: 5000})
				if err = os.WriteFile(filepath.Join(r.cfg.AgentDockHome, "execution", "permissions", "auto-review.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := r.Call(ctx, "file_edit", map[string]any{"action": "add", "path": "auto.txt", "content": "immutable"})
			if err != nil {
				t.Fatalf("auto review failed unexpectedly: %+v %v", result, err)
			}
			approvalID := stringArg(result, "approval_id")
			if approvalID == "" {
				t.Fatalf("missing approval reference %+v", result)
			}
			approval, err := r.permissions.Approval(ctx, approvalID)
			if err != nil || approval.CallID != stringArg(result, "call_id") || approval.DecidedBy != permission.ReviewerAuto {
				t.Fatalf("wrong root/reviewer %+v %v", approval, err)
			}
			content, readErr := os.ReadFile(filepath.Join(r.cfg.AgentDockDefaultDir, "auto.txt"))
			if mode == "approve" {
				if readErr != nil || string(content) != "immutable" || approval.DispatchCount != 1 || approval.Status != "succeeded" {
					t.Fatalf("approved request did not run once: %+v %v", approval, readErr)
				}
			} else {
				if !os.IsNotExist(readErr) || approval.DispatchCount != 0 || approval.Status != "rejected" || result["executed"] != false {
					t.Fatalf("denied review executed: %+v %v", approval, readErr)
				}
			}
			if len(r.pendingCalls) != 0 {
				t.Fatal("auto review stranded a pending request")
			}
			replay, err := r.RuntimeApprovalDecision(ctx, approvalID, "approve", false)
			if err != nil || replay["dispatched"] != false {
				t.Fatalf("auto review replay dispatched: %+v %v", replay, err)
			}
		})
	}
}

func TestExecutionProfileRevisionInvalidatesPreparedAllow(t *testing.T) {
	settings := permission.DefaultSettings()
	r, ctx := profileRuntime(t, settings, permission.Full)
	effective, err := r.permissions.Effective(ctx, activity.Binding{})
	if err != nil {
		t.Fatal(err)
	}
	p := &preparedExecution{spec: ToolSpec{Name: "read_file"}, args: map[string]any{"path": "read.txt"}, decision: permission.Decision{Effective: effective}}
	settings.Profile.Filesystem = permission.Deny
	if _, err = r.permissions.Update(ctx, permission.Change{Scope: "global", ExpectedRevision: 2, Settings: &settings}); err != nil {
		t.Fatal(err)
	}
	if err = r.revalidatePrepared(ctx, p); err == nil {
		t.Fatal("stale allowed request survived policy revision")
	}
}

func TestExecutionProfileSearchNeverInvokesExternalHelperOrGitConfig(t *testing.T) {
	settings := permission.DefaultSettings()
	settings.Profile.Network = permission.Deny
	settings.Profile.SandboxBoundary = permission.BoundaryWorkspace
	r, ctx := profileRuntime(t, settings, permission.Full)
	root := r.cfg.AgentDockDefaultDir
	if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte("needle"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.txt"), 0600); err != nil {
		t.Fatal(err)
	}
	// A restricted search must ignore implicit external configuration entirely.
	t.Setenv("RIPGREP_CONFIG_PATH", filepath.Join(t.TempDir(), "nonexistent-config"))
	result, err := r.Call(ctx, "search_text", map[string]any{"path": ".", "query": "needle"})
	if err != nil || result["engine"] != "go_fallback" || result["total_matches"] != 1 {
		t.Fatalf("restricted search invoked a helper or repository config: %+v %v", result, err)
	}
	result, err = r.Call(ctx, "list_dir", map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result["entries"])
	var entries []map[string]any
	if err = json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry["name"] == "sample.txt" {
			found = true
		}
	}
	if !found {
		t.Fatal("restricted directory listing read repository ignore configuration")
	}
}
