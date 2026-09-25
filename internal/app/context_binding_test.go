package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/agentinstructions"
	"github.com/uvwt/agentdock/internal/workspace"
)

func assertContextScope(t *testing.T, result Result, root, body string) workspace.Record {
	t.Helper()
	assertToolResultMatchestestOutputSchema(t, "agentdock_context", result)
	var selected workspace.Record
	var instructions agentinstructions.Snapshot
	if err := remarshal(result["workspace"], &selected); err != nil {
		t.Fatal(err)
	}
	if err := remarshal(result["instruction_files"], &instructions); err != nil {
		t.Fatal(err)
	}
	diagnostics, _ := result["context_diagnostics"].(map[string]any)
	guidance, _ := result["agentdock_guidance"].(map[string]any)
	if diagnostics["complete"] != true || !sameExistingTestPath(selected.Root, root) || !sameExistingTestPath(instructions.Workdir, root) ||
		result["workspace_id"] != selected.ID || guidance["workspace_id"] != selected.ID || !strings.Contains(instructionBodies(&instructions), body) {
		t.Fatalf("inconsistent context: expected directory=%q, selected=%q, instructions=%q, selected ID=%q, result ID=%v, guidance ID=%v, complete=%v, binding=%v, required rules present=%v", root, selected.Root, instructions.Workdir, selected.ID, result["workspace_id"], guidance["workspace_id"], diagnostics["complete"], diagnostics["binding_status"], strings.Contains(instructionBodies(&instructions), body))
	}
	return selected
}

func TestContextBindingSingleCallAndImplicitReuse(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("single-context")
	root := t.TempDir()
	writeInstructionFixture(t, root, "selected-project-rule")
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte("selected output"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := r.Call(ctx, "agentdock_context", map[string]any{"workdir": root})
	if err != nil {
		t.Fatal(err)
	}
	selected := assertContextScope(t, result, root, "selected-project-rule")
	if result["binding_updated"] != true {
		t.Fatalf("initial binding was not committed: %v", result["binding_updated"])
	}
	var committed activity.ConversationState
	if err := remarshal(result["conversation_state"], &committed); err != nil {
		t.Fatal(err)
	}
	if committed.WorkspaceID != selected.ID {
		t.Fatal("continuation disagrees with selected scope")
	}
	read, err := r.Call(ctx, "read_file", map[string]any{"path": "marker.txt"})
	if err != nil || read["content"] != "selected output" {
		t.Fatalf("next call did not inherit target: %v %v", read, err)
	}
	repeated, err := r.Call(ctx, "agentdock_context", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertContextScope(t, repeated, root, "selected-project-rule")
	var reused activity.ConversationState
	if err := remarshal(repeated["conversation_state"], &reused); err != nil {
		t.Fatal(err)
	}
	if repeated["binding_updated"] != false || reused.BindingRevision != committed.BindingRevision {
		t.Fatal("warm context rewrote an unchanged continuation")
	}
	if !sameExistingTestPath(r.ws.DefaultCWD(), r.cfg.AgentDockDefaultDir) {
		t.Fatal("context changed the device default")
	}
	call, err := r.activity.Call(ctx, stringArg(result, "call_id"))
	if err != nil || call.WorkspaceID != "" {
		t.Fatalf("entry audit was rewritten: %+v %v", call.Binding, err)
	}
}

func TestContextBindingSwitchKeepsHistoricalRoot(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("switch-context")
	a, b := t.TempDir(), t.TempDir()
	writeInstructionFixture(t, a, "rules-a")
	writeInstructionFixture(t, b, "rules-b")
	first, err := r.Call(ctx, "agentdock_context", map[string]any{"workdir": a})
	if err != nil {
		t.Fatal(err)
	}
	old := assertContextScope(t, first, a, "rules-a")
	second, err := r.Call(ctx, "agentdock_context", map[string]any{"workdir": b})
	if err != nil {
		t.Fatal(err)
	}
	selected := assertContextScope(t, second, b, "rules-b")
	if selected.ID == old.ID {
		t.Fatal("switch did not select a different workspace")
	}
	call, err := r.activity.Call(ctx, stringArg(second, "call_id"))
	if err != nil || call.WorkspaceID != old.ID {
		t.Fatalf("root entry changed after return: %+v %v", call.Binding, err)
	}
	if got := scopeRead(t, r, ctx)["workspace_id"]; got != selected.ID {
		t.Fatalf("next operation inherited %v", got)
	}
}

func pendingContext(t *testing.T, r *Runtime, ctx context.Context, root string) (*preparedExecution, Result) {
	t.Helper()
	scope, err := r.resolveExecutionScope(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.agentDockContext(activity.WithBinding(ctx, scope), false, root)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := result["context_diagnostics"].(map[string]any)
	if diagnostics["complete"] != false || diagnostics["binding_status"] != "pending" {
		t.Fatal("builder published complete before coordination")
	}
	return &preparedExecution{spec: ToolSpec{Name: "agentdock_context"}, state: executionObservation{entryBinding: scope, binding: scope}}, result
}

func TestContextBindingSameTargetConvergesDifferentTargetConflicts(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(map[bool]string{true: "same", false: "different"}[same], func(t *testing.T) {
			r := executionTestRuntime(t)
			ctx := scopeHost("competing-context")
			p, result := pendingContext(t, r, ctx, t.TempDir())
			var selected workspace.Record
			if err := remarshal(result["workspace"], &selected); err != nil {
				t.Fatal(err)
			}
			other := selected
			if !same {
				var err error
				other, err = r.workspaceRegistry.EnsureRoot(ctx, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
			}
			entry := p.state.entryBinding
			latest, err := r.conversations.UpdateBinding(ctx, entry.ConversationID, entry.BindingRevision, activity.ConversationState{WorkspaceID: other.ID})
			if err != nil {
				t.Fatal(err)
			}
			err = r.finalizeContextBinding(ctx, p, result)
			diagnostics := result["context_diagnostics"].(map[string]any)
			if same {
				if err != nil || diagnostics["complete"] != true || diagnostics["binding_status"] != "unchanged" || result["binding_updated"] != false {
					t.Fatalf("same-target convergence failed: %v %v", diagnostics, err)
				}
			} else {
				var failure *ToolError
				if !errors.As(err, &failure) || failure.Code != "CONTEXT_BINDING_CONFLICT" || diagnostics["complete"] != false {
					t.Fatalf("conflict hidden: %v %v", diagnostics, err)
				}
			}
			got, err := r.conversations.Get(ctx, entry.ConversationID)
			if err != nil || got.State != latest {
				t.Fatalf("competing continuation overwritten: %+v %v", got.State, err)
			}
			if p.state.binding != entry {
				t.Fatal("immutable root mutated")
			}
		})
	}
}

func TestContextBindingFailureNeverReportsComplete(t *testing.T) {
	for _, failure := range []string{"cancelled", "terminated", "workspace_changed", "incomplete"} {
		t.Run(failure, func(t *testing.T) {
			r := executionTestRuntime(t)
			ctx := scopeHost("failed-context")
			p, result := pendingContext(t, r, ctx, t.TempDir())
			diagnostics := result["context_diagnostics"].(map[string]any)
			switch failure {
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "terminated":
				if _, err := r.conversations.SetTerminated(activity.WithLocalManagement(ctx), p.state.entryBinding.ConversationID, true); err != nil {
					t.Fatal(err)
				}
			case "workspace_changed":
				var selected workspace.Record
				if err := remarshal(result["workspace"], &selected); err != nil {
					t.Fatal(err)
				}
				if _, err := r.workspaceRegistry.Register(ctx, workspace.RegisterInput{WorkspaceID: selected.ID, ExpectedRevision: &selected.RulesRevision, Name: "changed"}); err != nil {
					t.Fatal(err)
				}
			case "incomplete":
				diagnostics["components_complete"] = false
			}
			err := r.finalizeContextBinding(ctx, p, result)
			if diagnostics["complete"] != false || result["binding_updated"] != false {
				t.Fatalf("failed preparation reported complete: %v", diagnostics)
			}
			if failure != "incomplete" && err == nil {
				t.Fatal("coordination failure lost")
			}
			if failure == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation identity lost: %v", err)
			}
		})
	}
}

func TestContextBindingConcurrentConversationsAndUnattributed(t *testing.T) {
	r := executionTestRuntime(t)
	a, b := t.TempDir(), t.TempDir()
	writeInstructionFixture(t, a, "rules-a")
	writeInstructionFixture(t, b, "rules-b")
	var wg sync.WaitGroup
	for index, root := range []string{a, b} {
		wg.Add(1)
		go func(index int, root string) {
			defer wg.Done()
			ctx := scopeHost([]string{"context-a", "context-b"}[index])
			result, err := r.Call(ctx, "agentdock_context", map[string]any{"workdir": root})
			if err != nil {
				t.Error(err)
				return
			}
			var selected workspace.Record
			if err := remarshal(result["workspace"], &selected); err != nil || !sameExistingTestPath(selected.Root, root) {
				t.Errorf("crossed workspaces: %+v %v", selected, err)
			}
			again, err := r.Call(ctx, "agentdock_context", nil)
			if err != nil || again["workspace_id"] != selected.ID {
				t.Errorf("implicit context changed: %v", err)
			}
		}(index, root)
	}
	wg.Wait()
	result, err := r.Call(context.Background(), "agentdock_context", map[string]any{"workdir": a})
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := result["context_diagnostics"].(map[string]any)
	if diagnostics["complete"] != true || diagnostics["binding_status"] != "unattributed" || stringArg(result, "conversation_id") != "" || result["binding_updated"] != false {
		t.Fatal("unattributed context inferred continuation")
	}
	conversations, err := r.conversations.List(context.Background())
	if err != nil || len(conversations) != 2 {
		t.Fatalf("unexpected inferred conversation: %d %v", len(conversations), err)
	}
}

func TestContextDirectoryComparisonRejectsDifferentTargets(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	if sameExistingTestPath(left, right) {
		t.Fatal("context directory identity comparison accepted a different target")
	}
	if !sameExistingTestPath(left, filepath.Join(left, ".")) {
		t.Fatal("context directory identity comparison rejected the same directory")
	}
}
