package permission

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
)

func TestDefaultPolicyAndExplicitDeny(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	facts := Facts{Tool: "file_edit"}
	d, err := store.Decide(ctx, facts)
	if err != nil || d.Mode != Rules || d.Effect != Ask {
		t.Fatalf("unsafe default: %+v %v", d, err)
	}
	facts.ReadOnly = true
	d, err = store.Decide(ctx, facts)
	if err != nil || d.Effect != Allow {
		t.Fatal("known read-only operation requires approval")
	}
	if _, err = store.Update(ctx, Change{Scope: "global", Mode: Full, ExpectedRevision: 1}); err == nil {
		t.Fatal("full mode enabled without explicit confirmation")
	}
	p, err := store.Update(ctx, Change{Scope: "global", Mode: Full, ExpectedRevision: 1, ConfirmFull: true})
	if err != nil {
		t.Fatal(err)
	}
	rules := []Rule{{ID: "forbid_edits", Tool: "file_edit", Effect: Deny, Reason: "user forbidden"}}
	if _, err = store.Update(ctx, Change{ExpectedRevision: p.Revision, Rules: &rules}); err != nil {
		t.Fatal(err)
	}
	d, err = store.Decide(ctx, facts)
	if err != nil || d.Effect != Deny {
		t.Fatal("full mode bypassed explicit deny")
	}
}
func TestApprovalClaimIsDurableAndAtMostOnce(t *testing.T) {
	root := t.TempDir()
	first, _ := New(root)
	second, _ := New(root)
	ctx := context.Background()
	id, _ := activity.NewExecutionID("call_")
	a, err := first.Create(ctx, Approval{Binding: activity.Binding{CallID: id}, Tool: "file_edit", PolicyRevision: 1, Operation: "fixed display"})
	if err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := first
			if i%2 == 1 {
				s = second
			}
			_, claimed, err := s.Claim(ctx, a.ID)
			if err != nil {
				t.Error(err)
			}
			if claimed {
				count.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("dispatched %d times", count.Load())
	}
	reopened, _ := New(root)
	saved, claimed, err := reopened.Claim(ctx, a.ID)
	if err != nil || claimed || saved.DispatchCount != 1 {
		t.Fatal("restart claimed an already dispatched request")
	}
}
func TestApprovalPolicyRevisionInvalidation(t *testing.T) {
	s, _ := New(t.TempDir())
	ctx := context.Background()
	id, _ := activity.NewExecutionID("call_")
	a, err := s.Create(ctx, Approval{Binding: activity.Binding{CallID: id}, Tool: "file_edit", PolicyRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(ctx, Change{Scope: "global", Mode: ReadOnly, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	saved, claimed, err := s.Claim(ctx, a.ID)
	if err == nil || claimed || saved.Status != "expired" {
		t.Fatal("stale approval dispatched")
	}
}
func TestApprovalWorkspaceRuleIsScoped(t *testing.T) {
	s, _ := New(t.TempDir())
	ctx := context.Background()
	id, _ := activity.NewExecutionID("call_")
	a, err := s.Create(ctx, Approval{Binding: activity.Binding{CallID: id, WorkspaceID: "wsp_one"}, Tool: "file_edit", Action: "add", PolicyRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	saved, claimed, err := s.ClaimWithWorkspaceRule(ctx, a.ID, true)
	if err != nil || !claimed || saved.GrantedRuleID == "" {
		t.Fatalf("grant failed %+v %v", saved, err)
	}
	d, err := s.Decide(ctx, Facts{Binding: activity.Binding{WorkspaceID: "wsp_one"}, Tool: "file_edit", Action: "add"})
	if err != nil || d.Effect != Allow {
		t.Fatal("workspace rule did not apply")
	}
	other, err := s.Decide(ctx, Facts{Binding: activity.Binding{WorkspaceID: "wsp_two"}, Tool: "file_edit", Action: "add"})
	if err != nil || other.Effect != Ask {
		t.Fatal("workspace rule leaked")
	}
}
