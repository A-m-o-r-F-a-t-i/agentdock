package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

func autoApproval(t *testing.T, s *Store) Approval {
	t.Helper()
	p, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	id, err := activity.NewExecutionID("call_")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Create(context.Background(), Approval{Binding: activity.Binding{CallID: id, WorkspaceID: "wsp_review"}, Tool: "file_edit", PolicyRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func configureTestReviewer(t *testing.T, s *Store, mode string, timeout int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(ReviewerConfig{Command: exe, Args: []string{"-test.run=^TestReviewerSubprocess$", "--", mode}, TimeoutMS: timeout})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.root, "auto-review.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReviewerSubprocess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	var request ReviewRequest
	if json.NewDecoder(os.Stdin).Decode(&request) != nil {
		os.Exit(10)
	}
	if os.Getenv("AGENTDOCK_REVIEW_TEST_SECRET") != "" {
		os.Exit(11)
	}
	mode := os.Args[len(os.Args)-1]
	response := ReviewResult{ApprovalID: request.Approval.ID, CallID: request.Approval.CallID, Decision: "approve", Reason: "isolated test reviewer"}
	switch mode {
	case "reject":
		response.Decision = "reject"
	case "wrong":
		response.CallID = "call_other"
	case "invalid":
		fmt.Print("not JSON")
		os.Exit(0)
	case "duplicate":
		fmt.Printf(`{"approval_id":%q,"call_id":%q,"decision":"reject","decision":"approve","reason":"duplicate"}`, response.ApprovalID, response.CallID)
		os.Exit(0)
	case "oversize":
		fmt.Print(strings.Repeat("x", maxReviewResponseBytes+1))
		os.Exit(0)
	case "wait":
		time.Sleep(5 * time.Second)
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	os.Exit(0)
}

func TestReviewerProtocolRejectsFailures(t *testing.T) {
	t.Setenv("AGENTDOCK_REVIEW_TEST_SECRET", "must-not-inherit")
	for _, mode := range []string{"approve", "reject", "wrong", "invalid", "duplicate", "oversize", "wait"} {
		t.Run(mode, func(t *testing.T) {
			settings := DefaultSettings()
			settings.Reviewer = ReviewerAuto
			s := settingsStore(t, settings)
			a := autoApproval(t, s)
			timeout := 3000
			if mode == "wait" {
				timeout = 100
			}
			configureTestReviewer(t, s, mode, timeout)
			started := time.Now()
			result, err := s.RunReviewer(context.Background(), ReviewRequest{SchemaVersion: 1, Approval: a, FixedRequest: `{"action":"add","path":"fixture.txt"}`, Redacted: true})
			if mode == "approve" || mode == "reject" {
				if err != nil || result.Decision != mode {
					t.Fatalf("unexpected result %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatalf("invalid reviewer result accepted: %+v", result)
			}
			if mode == "wait" && time.Since(started) > 2*time.Second {
				t.Fatal("reviewer timeout was not bounded")
			}
			saved, _ := s.Approval(context.Background(), a.ID)
			if saved.DispatchCount != 0 {
				t.Fatal("review transport dispatched a tool")
			}
		})
	}
}

func TestReviewerMissingConfigIncompleteAndCancelled(t *testing.T) {
	settings := DefaultSettings()
	settings.Reviewer = ReviewerAuto
	s := settingsStore(t, settings)
	a := autoApproval(t, s)
	request := ReviewRequest{SchemaVersion: 1, Approval: a, FixedRequest: "{}", Redacted: true}
	if _, err := s.RunReviewer(context.Background(), request); err == nil {
		t.Fatal("missing reviewer approved")
	}
	configureTestReviewer(t, s, "approve", 3000)
	request.FixedRequest = strings.Repeat("x", MaxReviewRequestBytes+1)
	if _, err := s.RunReviewer(context.Background(), request); err == nil {
		t.Fatal("oversized request approved")
	}
	request.FixedRequest = "{}"
	request.Redacted = false
	if _, err := s.RunReviewer(context.Background(), request); err == nil {
		t.Fatal("unredacted request approved")
	}
	request.Redacted = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.RunReviewer(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not preserved", err)
	}
}

func TestReviewerClaimIdentityVerdictAndAtMostOnce(t *testing.T) {
	settings := DefaultSettings()
	settings.Reviewer = ReviewerAuto
	s := settingsStore(t, settings)
	ctx := context.Background()
	a := autoApproval(t, s)
	if _, claimed, err := s.Claim(ctx, a.ID); err == nil || claimed {
		t.Fatal("user claimed auto approval")
	}
	if _, claimed, err := s.ClaimReviewed(ctx, a.ID, false, ReviewerAuto); err == nil || claimed {
		t.Fatal("approval without review")
	}
	review := ReviewResult{ApprovalID: a.ID, CallID: a.CallID, Decision: "approve", Reason: "scoped fixture"}
	if err := s.RecordReview(ctx, a.ID, review); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReview(ctx, a.ID, review); err == nil {
		t.Fatal("review overwritten")
	}
	if _, claimed, err := s.ClaimReviewed(ctx, a.ID, true, ReviewerAuto); err == nil || claimed {
		t.Fatal("auto review created permanent grant")
	}
	var count atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, claimed, err := s.ClaimReviewed(ctx, a.ID, false, ReviewerAuto)
			if err != nil {
				t.Error(err)
			}
			if claimed {
				count.Add(1)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal("duplicate dispatch", count.Load())
	}
	final, err := s.Settle(ctx, a.ID, "succeeded", "done")
	if err != nil || final.DecidedBy != ReviewerAuto || final.DispatchCount != 1 {
		t.Fatalf("lost reviewer identity %+v %v", final, err)
	}
	p, _ := s.Get(ctx)
	if len(p.Rules) != 0 {
		t.Fatal("automatic review persisted an allow rule")
	}
}

func TestReviewerRejectAndRevisionChangePreventClaim(t *testing.T) {
	settings := DefaultSettings()
	settings.Reviewer = ReviewerAuto
	s := settingsStore(t, settings)
	ctx := context.Background()
	a := autoApproval(t, s)
	if err := s.RecordReview(ctx, a.ID, ReviewResult{ApprovalID: a.ID, CallID: a.CallID, Decision: "reject", Reason: "no"}); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := s.ClaimReviewed(ctx, a.ID, false, ReviewerAuto); err == nil || claimed {
		t.Fatal("negative verdict approved")
	}
	a = autoApproval(t, s)
	if err := s.RecordReview(ctx, a.ID, ReviewResult{ApprovalID: a.ID, CallID: a.CallID, Decision: "approve", Reason: "before revision"}); err != nil {
		t.Fatal(err)
	}
	settings.Profile.Filesystem = Deny
	if _, err := s.Update(ctx, Change{Scope: "global", ExpectedRevision: 2, Settings: &settings}); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := s.ClaimReviewed(ctx, a.ID, false, ReviewerAuto); !errors.Is(err, ErrApprovalExpired) || claimed {
		t.Fatal("stale review approved", err)
	}
}

func TestReviewerStrictJSON(t *testing.T) {
	for _, text := range []string{`{"decision":"approve","extra":true}`, `{"decision":"reject","decision":"approve"}`, `{} {}`, `{"reason":{"reason":1,"reason":2}}`} {
		if err := strictJSON([]byte(text), new(ReviewResult)); err == nil {
			t.Fatalf("accepted %s", text)
		}
	}
}
