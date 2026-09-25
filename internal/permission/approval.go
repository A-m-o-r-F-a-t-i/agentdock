package permission

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/fs/filelock"
)

var ErrApprovalNotFound = errors.New("approval not found")
var ErrApprovalExpired = errors.New("approval expired or no longer matches the effective policy")

type Approval struct {
	Reviewer       string     `json:"approval_reviewer,omitempty"`
	Settings       *Settings  `json:"permission_settings,omitempty"`
	ReviewDecision string     `json:"review_decision,omitempty"`
	ReviewReason   string     `json:"review_reason,omitempty"`
	ReviewedAt     *time.Time `json:"reviewed_at,omitempty"`
	OwnerPID       int        `json:"owner_pid,omitempty"`
	OwnerInstance  string     `json:"owner_instance,omitempty"`
	activity.Binding
	ID                string     `json:"approval_id"`
	SchemaVersion     int        `json:"schema_version"`
	Tool              string     `json:"tool"`
	Action            string     `json:"action,omitempty"`
	Operation         string     `json:"operation"`
	ScopeDescription  string     `json:"scope_description"`
	RuleID            string     `json:"rule_id"`
	Reason            string     `json:"reason"`
	Mode              string     `json:"mode"`
	PolicyRevision    uint64     `json:"policy_revision"`
	WorkspaceRevision int        `json:"workspace_revision,omitempty"`
	Status            string     `json:"status"`
	CreatedAt         time.Time  `json:"created_at"`
	ExpiresAt         time.Time  `json:"expires_at"`
	DecidedAt         *time.Time `json:"decided_at,omitempty"`
	DecidedBy         string     `json:"decided_by,omitempty"`
	DispatchCount     int        `json:"dispatch_count"`
	GrantedRuleID     string     `json:"granted_rule_id,omitempty"`
	Summary           string     `json:"summary,omitempty"`
}

func (s *Store) approvalPath(id string) (string, error) {
	if !validID.MatchString(id) || !strings.HasPrefix(id, "approval_") {
		return "", errors.New("invalid approval identifier")
	}
	return filepath.Join(s.root, "approvals", id+".json"), nil
}
func (s *Store) loadApproval(id string) (Approval, error) {
	path, err := s.approvalPath(id)
	if err != nil {
		return Approval{}, err
	}
	var a Approval
	if err = readJSON(path, &a); os.IsNotExist(err) {
		return a, ErrApprovalNotFound
	} else if err != nil {
		return a, err
	}
	if a.ID != id || a.SchemaVersion != 1 || a.CallID == "" || a.Binding.Validate() != nil {
		return Approval{}, errors.New("invalid approval record")
	}
	return a, nil
}

// Create persists only the already-redacted, bounded display snapshot. Original
// immutable arguments remain in the runtime's pending request closure in memory.
func (s *Store) Create(ctx context.Context, a Approval) (Approval, error) {
	err := s.locked(ctx, func() error {
		p, err := s.loadPolicy()
		if err != nil {
			return err
		}
		if a.PolicyRevision != p.Revision {
			return ErrRevision
		}
		if a.CallID == "" || a.Binding.Validate() != nil {
			return errors.New("approval requires a valid call binding")
		}
		if len(a.Operation) > 16384 || len(a.ScopeDescription) > 4096 || len(a.Reason) > 2048 {
			return errors.New("approval snapshot is too large")
		}
		if a.ID == "" {
			a.ID, err = activity.NewExecutionID("approval_")
			if err != nil {
				return err
			}
		}
		path, err := s.approvalPath(a.ID)
		if err != nil {
			return err
		}
		if _, err = os.Lstat(path); err == nil {
			return errors.New("approval ID already exists")
		} else if !os.IsNotExist(err) {
			return err
		}
		effective := effectivePolicy(p, a.Binding)
		if effective.Settings.Approval.Mode == Never {
			return errors.New("approval policy never does not accept pending requests")
		}
		if a.Reviewer == "" {
			a.Reviewer = effective.Settings.Reviewer
		}
		if a.Reviewer != effective.Settings.Reviewer {
			return errors.New("approval reviewer differs from effective settings")
		}
		a.Settings = &effective.Settings
		if a.Reviewer != ReviewerUser && a.Reviewer != ReviewerAuto {
			return errors.New("invalid approval reviewer")
		}
		a.SchemaVersion = 1
		a.OwnerPID = os.Getpid()
		a.OwnerInstance = s.instance
		a.CreatedAt = time.Now().UTC()
		a.ExpiresAt = a.CreatedAt.Add(15 * time.Minute)
		a.Status = "pending"
		a.DispatchCount = 0
		return writeJSON(ctx, path, a)
	})
	return a, err
}
func (s *Store) Approval(ctx context.Context, id string) (Approval, error) {
	var a Approval
	err := s.locked(ctx, func() error { var err error; a, err = s.loadApproval(id); return err })
	return a, err
}
func (s *Store) Approvals(ctx context.Context) ([]Approval, error) {
	items := []Approval{}
	err := s.locked(ctx, func() error {
		entries, err := os.ReadDir(filepath.Join(s.root, "approvals"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "approval_") || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			a, err := s.loadApproval(strings.TrimSuffix(entry.Name(), ".json"))
			if err != nil {
				return err
			}
			items = append(items, a)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
		return nil
	})
	return items, err
}

// Claim is a durable compare-and-swap. The caller may dispatch only when claimed
// is true. A crash after the claim is deliberately not retried automatically.
func (s *Store) Claim(ctx context.Context, id string) (a Approval, claimed bool, err error) {
	return s.ClaimWithWorkspaceRule(ctx, id, false)
}
func (s *Store) ClaimWithWorkspaceRule(ctx context.Context, id string, grantWorkspace bool) (a Approval, claimed bool, err error) {
	return s.ClaimReviewed(ctx, id, grantWorkspace, ReviewerUser)
}
func (s *Store) ClaimReviewed(ctx context.Context, id string, grantWorkspace bool, reviewer string) (a Approval, claimed bool, err error) {
	err = s.locked(ctx, func() error {
		var err error
		a, err = s.loadApproval(id)
		if err != nil {
			return err
		}
		if a.Status != "pending" {
			return nil
		}
		p, err := s.loadPolicy()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if !now.Before(a.ExpiresAt) || a.PolicyRevision != p.Revision {
			a.Status = "expired"
			a.Summary = "授权已过期或权限策略已变化，原操作未派发。"
			a.DecidedAt = &now
			path, _ := s.approvalPath(id)
			if err = writeJSON(ctx, path, a); err != nil {
				return err
			}
			return ErrApprovalExpired
		}
		selected := a.Reviewer
		if selected == "" {
			selected = ReviewerUser
		}
		if reviewer != selected || (reviewer != ReviewerUser && reviewer != ReviewerAuto) {
			return errors.New("approval reviewer mismatch")
		}
		if reviewer == ReviewerAuto && (grantWorkspace || a.ReviewDecision != "approve") {
			return errors.New("auto_review requires a recorded affirmative verdict and cannot create persistent grants")
		}
		commitCtx := ctx
		if grantWorkspace {
			if a.WorkspaceID == "" {
				return errors.New("workspace rule requires a fixed workspace")
			}
			ruleID := fmt.Sprintf("allow_%d", now.UnixNano())
			p.Rules = append(p.Rules, Rule{ID: ruleID, Tool: a.Tool, Action: a.Action, WorkspaceID: a.WorkspaceID, Effect: Allow, Reason: "用户在审批时允许此工作区的同类工具操作。"})
			p.Revision++
			p.UpdatedAt = now
			if err = validatePolicy(p); err != nil {
				return err
			}
			if err = writeJSON(ctx, filepath.Join(s.root, "policy.json"), p); err != nil {
				return err
			}
			// The grant is durable. Finish its associated claim rather than
			// reporting cancellation as though no permission was committed.
			commitCtx = context.WithoutCancel(ctx)
			a.PolicyRevision = p.Revision
			a.GrantedRuleID = ruleID
		}
		a.Status = "dispatched"
		a.DispatchCount = 1
		a.DecidedAt = &now
		a.DecidedBy = "local_user"
		if reviewer == ReviewerAuto {
			a.DecidedBy = ReviewerAuto
		}
		path, _ := s.approvalPath(id)
		if err = writeJSON(commitCtx, path, a); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return a, claimed, err
}

// Settle never re-opens a claimed approval. Repeated decisions are safe no-ops.
func (s *Store) Settle(ctx context.Context, id, status, summary string) (Approval, error) {
	return s.SettleReviewed(ctx, id, status, summary, "local_user")
}
func (s *Store) SettleReviewed(ctx context.Context, id, status, summary, actor string) (Approval, error) {
	if actor != "local_user" && actor != ReviewerAuto {
		return Approval{}, errors.New("invalid approval actor")
	}
	var a Approval
	err := s.locked(ctx, func() error {
		var err error
		a, err = s.loadApproval(id)
		if err != nil {
			return err
		}
		switch status {
		case "rejected", "expired":
			if a.Status != "pending" {
				return nil
			}
		case "succeeded", "failed", "cancelled", "unknown":
			if a.Status != "dispatched" {
				return nil
			}
		default:
			return errors.New("invalid approval terminal status")
		}
		a.Status = status
		a.Summary = summary
		if len(summary) > 4096 {
			return errors.New("approval summary is too large")
		}
		now := time.Now().UTC()
		a.DecidedAt = &now
		if a.DecidedBy == "" {
			a.DecidedBy = actor
		}
		path, _ := s.approvalPath(id)
		return writeJSON(ctx, path, a)
	})
	return a, err
}

// Recover is invoked once when the owning runtime starts. It must not dispatch
// persisted requests: their original parameters were intentionally not stored.
func (s *Store) Recover(ctx context.Context) ([]Approval, error) {
	items, err := s.Approvals(ctx)
	if err != nil {
		return nil, err
	}
	recovered := []Approval{}
	for _, a := range items {
		if filelock.ProcessAlive(a.OwnerPID) {
			continue
		}
		status := ""
		summary := ""
		if a.Status == "pending" {
			status = "expired"
			summary = "服务已重启，内存中的固定请求已失效，未自动执行。"
		}
		if a.Status == "dispatched" {
			status = "unknown"
			summary = "服务重启前已派发，副作用结果尚未确认。请核对实际状态，勿自动重试。"
		}
		if status == "" {
			continue
		}
		changed, err := s.Settle(ctx, a.ID, status, summary)
		if err != nil {
			return recovered, err
		}
		recovered = append(recovered, changed)
	}
	return recovered, nil
}
