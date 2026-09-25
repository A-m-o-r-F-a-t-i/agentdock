package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/uvwt/agentdock/internal/permission"
)

func (r *Runtime) autoReviewPrepared(ctx context.Context, p *preparedExecution, approval permission.Approval) (Result, error) {
	reviewCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.commandCtx, cancel)
	defer cancel()
	defer stop()
	verdict := permission.ReviewResult{ApprovalID: approval.ID, CallID: approval.CallID, Decision: "reject", Reason: "固定请求过大或无法完整提供给独立审查器，未自动批准。"}
	raw, encodeErr := json.Marshal(p.args)
	if encodeErr == nil && len(raw) <= permission.MaxReviewRequestBytes {
		redacted := r.executionRedactor(p.args).Text(string(raw), 1<<20)
		reviewed, err := r.permissions.RunReviewer(reviewCtx, permission.ReviewRequest{SchemaVersion: 1, Approval: approval, FixedRequest: redacted, Redacted: true})
		if err == nil {
			verdict = reviewed
			verdict.Reason = r.executionRedactor(p.args).Text(verdict.Reason, 2048)
		} else {
			verdict.Reason = "自动审查未通过：" + r.executionRedactor(p.args).Text(err.Error(), 1800)
		}
	}
	if reviewCtx.Err() != nil {
		verdict.Decision, verdict.Reason = "reject", "自动审查已取消或服务正在关闭，原操作未执行。"
	}
	// Cancellation must not strand an unattended request in the human queue.
	auditCtx, auditCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer auditCancel()
	if err := r.permissions.RecordReview(auditCtx, approval.ID, verdict); err != nil {
		_, _ = r.runtimeApprovalDecision(auditCtx, approval.ID, "reject", false, permission.ReviewerAuto)
		return nil, r.executionError(err, p.state)
	}
	decisionCtx := ctx
	if verdict.Decision == "reject" {
		decisionCtx = auditCtx
	}
	result, err := r.runtimeApprovalDecision(decisionCtx, approval.ID, verdict.Decision, false, permission.ReviewerAuto)
	if err != nil {
		_, _ = r.runtimeApprovalDecision(auditCtx, approval.ID, "reject", false, permission.ReviewerAuto)
		return nil, r.executionError(err, p.state)
	}
	if verdict.Decision == "reject" {
		return r.decorateExecution(Result{"status": "approval_rejected", "executed": false, "approval_id": approval.ID, "approval": result["approval"]}, p), nil
	}
	return result, nil
}
